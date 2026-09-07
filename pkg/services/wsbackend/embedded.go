package wsbackend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// EmbeddedBackend implements FileOperator, BashOperator, and DirLister
// using the local filesystem and os/exec. It operates over one or more
// mounted directories; the first mount is the primary one (relative tool
// paths and the default bash cwd resolve against it), while absolute paths
// are accepted when they fall inside any mount.
//
// File mutations (Write, Edit) are serialised per resolved path via
// an internal mutex map. No two goroutines can mutate the same file
// concurrently; reads are lock-free.
type EmbeddedBackend struct {
	mountsMu  sync.Mutex
	mounts    []mount
	fileLocks map[string]*sync.Mutex
	locksMu   sync.Mutex
}

// mount is one mounted directory: path is the canonical absolute path as
// configured; real is its symlink-resolved form used for containment.
type mount struct {
	path string
	real string
}

// NewEmbeddedBackend returns an EmbeddedBackend with the given mounted
// directories. Each path is expanded (~), canonicalised and validated; a
// path that fails validation is logged and skipped. Must be used instead of
// direct struct literals so that the per-file mutex map is not nil.
func NewEmbeddedBackend(mounts []string) *EmbeddedBackend {
	b := &EmbeddedBackend{
		fileLocks: make(map[string]*sync.Mutex),
	}
	for _, raw := range mounts {
		if _, err := b.AddMount(raw); err != nil {
			log.Printf("[wsbackend] mount %q: %v", raw, err)
		}
	}
	if len(b.mounts) == 0 {
		log.Printf("[wsbackend] warning: no valid mounts configured")
	}
	return b
}

// Mounts returns a copy of the currently mounted directory paths.
func (b *EmbeddedBackend) Mounts() []string {
	b.mountsMu.Lock()
	defer b.mountsMu.Unlock()
	out := make([]string, len(b.mounts))
	for i, m := range b.mounts {
		out[i] = m.path
	}
	return out
}

// AddMount validates and appends a mounted directory. The path must exist
// and be a directory; duplicates (same symlink-resolved path) are rejected.
// Returns the canonical absolute path added.
func (b *EmbeddedBackend) AddMount(raw string) (string, error) {
	m, err := validateMount(raw)
	if err != nil {
		return "", err
	}
	b.mountsMu.Lock()
	defer b.mountsMu.Unlock()
	for _, existing := range b.mounts {
		if existing.real == m.real {
			return "", fmt.Errorf("mount %s already mounted (as %s)", raw, existing.path)
		}
	}
	b.mounts = append(b.mounts, m)
	return m.path, nil
}

// RemoveMount detaches the mount matching the given path (~ expansion and
// cleaning applied; symlink-resolved form compared too). The last remaining
// mount cannot be removed — relative paths and the default bash cwd need a
// primary. Reports whether a mount was removed.
func (b *EmbeddedBackend) RemoveMount(raw string) (bool, error) {
	if raw == "" {
		return false, fmt.Errorf("mount path is empty")
	}
	expanded, err := expandHome(raw)
	if err != nil {
		return false, err
	}
	abs, err := filepath.Abs(filepath.Clean(expanded))
	if err != nil {
		return false, fmt.Errorf("resolve mount: %w", err)
	}
	real := abs
	if r, evalErr := filepath.EvalSymlinks(abs); evalErr == nil && r != "" {
		real = r
	}

	b.mountsMu.Lock()
	defer b.mountsMu.Unlock()
	for i, m := range b.mounts {
		if m.path == abs || m.real == real {
			if len(b.mounts) == 1 {
				return false, fmt.Errorf("cannot remove the last remaining mount")
			}
			b.mounts = append(b.mounts[:i], b.mounts[i+1:]...)
			return true, nil
		}
	}
	return false, fmt.Errorf("%s is not mounted", abs)
}

// ReplaceMounts atomically replaces the whole mount set with the given
// validated paths. Used by Restore to re-apply persisted mounts.
func (b *EmbeddedBackend) ReplaceMounts(paths []string) error {
	var next []mount
	for _, raw := range paths {
		m, err := validateMount(raw)
		if err != nil {
			return err
		}
		for _, existing := range next {
			if existing.real == m.real {
				return fmt.Errorf("duplicate mount %s", raw)
			}
		}
		next = append(next, m)
	}
	if len(next) == 0 {
		return fmt.Errorf("at least one mount is required")
	}
	b.mountsMu.Lock()
	b.mounts = next
	b.mountsMu.Unlock()
	return nil
}

// currentMounts snapshots the mount slice.
func (b *EmbeddedBackend) currentMounts() []mount {
	b.mountsMu.Lock()
	defer b.mountsMu.Unlock()
	return b.mounts
}

// primary returns the first mount's path — the base for relative tool paths
// and the default bash cwd. Empty when no valid mount is configured.
func (b *EmbeddedBackend) primary() string {
	b.mountsMu.Lock()
	defer b.mountsMu.Unlock()
	if len(b.mounts) == 0 {
		return ""
	}
	return b.mounts[0].path
}

// resolve resolves a raw tool path against the mounts. Relative paths
// resolve against the primary (first) mount; absolute paths are accepted
// when they fall inside any mounted directory. Symlink-aware: a resolved
// target is checked against the mount's symlink-resolved root, falling back
// to a plain prefix check for not-yet-existing paths.
// resolve validates raw against the mounts and returns the absolute path to
// use.
//
// Containment rule: a path is accepted when its symlink-resolved form falls
// inside a mount, or — because a symlinked entry may legitimately point
// elsewhere, e.g. a program directory symlinked into the workspace — when its
// logical form still falls inside a mount. Only the second case can leave the
// mount. Syntactic traversal ("..") is always rejected: paths are Cleaned
// first, so an escape attempt loses the mount prefix and fails both checks.
func (b *EmbeddedBackend) resolve(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("path is empty")
	}
	mounts := b.currentMounts()
	if len(mounts) == 0 {
		return "", fmt.Errorf("no mounts configured")
	}

	expanded, err := expandHome(raw)
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	abs, err := filepath.Abs(filepath.Clean(expanded))
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	if filepath.IsAbs(filepath.Clean(expanded)) {
		real, evalErr := filepath.EvalSymlinks(abs)
		if evalErr != nil {
			// Path does not exist yet — plain prefix check per mount.
			for _, m := range mounts {
				if hasPathPrefix(abs, m.path) {
					return abs, nil
				}
			}
			return "", &EscapeError{Path: raw}
		}
		for _, m := range mounts {
			if hasPathPrefix(real, m.real) {
				return abs, nil
			}
		}
		// Symlink escape is allowed — see resolve.
		for _, m := range mounts {
			if hasPathPrefix(abs, m.path) {
				return abs, nil
			}
		}
		return "", &EscapeError{Path: raw}
	}

	// Relative: resolve against the primary mount.
	p := mounts[0]
	joined := filepath.Clean(filepath.Join(p.path, filepath.Clean(expanded)))
	real, evalErr := filepath.EvalSymlinks(joined)
	if evalErr != nil {
		if hasPathPrefix(joined, p.path) {
			return joined, nil
		}
		return "", &EscapeError{Path: raw}
	}
	if hasPathPrefix(real, p.real) {
		return joined, nil
	}
	// A symlinked component may legitimately point outside the mount — a
	// program or workspace directory symlinked in from elsewhere. Allow it
	// when the logical path is still inside the mount. Syntactic traversal
	// ("..") stays rejected: joined is Cleaned, so anything that escapes
	// loses the mount prefix.
	if hasPathPrefix(joined, p.path) {
		return joined, nil
	}
	return "", &EscapeError{Path: raw}
}

// fileLock returns a function that unlocks the per-file mutex for path.
func (b *EmbeddedBackend) fileLock(path string) func() {
	b.locksMu.Lock()
	mu, ok := b.fileLocks[path]
	if !ok {
		mu = &sync.Mutex{}
		b.fileLocks[path] = mu
	}
	b.locksMu.Unlock()

	mu.Lock()
	return mu.Unlock
}

// Read reads a file within the workspace root. offset is 1-indexed (0 = no
// offset), limit caps the number of lines returned (0 = no limit). The
// returned string contains only the requested slice.
func (b *EmbeddedBackend) Read(ctx context.Context, path string, offset, limit int) (string, error) {
	resolved, err := b.resolve(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	content := string(data)
	if offset <= 0 && limit <= 0 {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if offset < 1 {
		offset = 1
	}
	if offset > len(lines) {
		return "", nil
	}
	lines = lines[offset-1:]
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
	}
	return strings.Join(lines, "\n"), nil
}

func (b *EmbeddedBackend) Write(ctx context.Context, path, content string) error {
	resolved, err := b.resolve(path)
	if err != nil {
		return err
	}

	unlock := b.fileLock(resolved)
	defer unlock()

	if err := os.MkdirAll(filepath.Dir(resolved), 0755); err != nil {
		return fmt.Errorf("create parent dirs: %w", err)
	}

	return os.WriteFile(resolved, []byte(content), 0644)
}

// Edit performs an atomic (read -> check -> write) replacement under the
// per-file lock. If the exact oldStr is not found, it falls back to a
// Unicode-normalised fuzzy match (curly quotes -> straight, em-dashes -> --).
func (b *EmbeddedBackend) Edit(ctx context.Context, path, oldStr, newStr string) error {
	resolved, err := b.resolve(path)
	if err != nil {
		return err
	}

	unlock := b.fileLock(resolved)
	defer unlock()

	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("read file for edit: %w", err)
	}
	content := string(data)

	// Exact match.
	count := strings.Count(content, oldStr)
	if count == 1 {
		newContent := strings.Replace(content, oldStr, newStr, 1)
		return os.WriteFile(resolved, []byte(newContent), 0644)
	}
	if count > 1 {
		return fmt.Errorf("old_string appears %d times in %s - provide a more unique match", count, path)
	}

	// Fuzzy fallback.
	normContent := NormalizeQuotes(content)
	normOld := NormalizeQuotes(oldStr)
	normCount := strings.Count(normContent, normOld)
	if normCount == 0 {
		return fmt.Errorf("old_string not found in %s (exact and fuzzy)", path)
	}
	if normCount > 1 {
		return fmt.Errorf("old_string appears %d times in %s after fuzzy normalization - provide a more unique match", normCount, path)
	}

	idx := strings.Index(normContent, normOld)
	newContent := content[:idx] + newStr + content[idx+len(oldStr):]
	return os.WriteFile(resolved, []byte(newContent), 0644)
}

// List returns the contents of a directory as []DirEntry, sorted by name
// (directories first, then files). Implements [DirLister].
func (b *EmbeddedBackend) List(ctx context.Context, path string) ([]DirEntry, error) {
	resolved, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}
	result := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.Name() == "" {
			continue
		}
		// os.ReadDir reports a symlink's own type, not its target's, so a
		// symlink to a directory comes back IsDir=false and callers would
		// never descend into it. Stat the entry to see through the link; a
		// broken symlink simply stays a non-directory.
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if info, statErr := os.Stat(filepath.Join(resolved, e.Name())); statErr == nil {
				isDir = info.IsDir()
			}
		}
		result = append(result, DirEntry{
			Name:  e.Name(),
			IsDir: isDir,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// Remove deletes a file or directory at the given path. If the path is a
// directory, it is removed recursively along with all contents.
func (b *EmbeddedBackend) Remove(ctx context.Context, path string) error {
	resolved, err := b.resolve(path)
	if err != nil {
		return err
	}

	// Security: ensure the resolved path is within one of the mounts.
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	contained := false
	for _, m := range b.currentMounts() {
		if hasPathPrefix(absResolved, m.path) || hasPathPrefix(absResolved, m.real) {
			contained = true
			break
		}
	}
	if !contained {
		return &EscapeError{Path: path}
	}

	// Check if the path exists.
	_, err = os.Stat(absResolved)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("path %q not found", path)
		}
		return fmt.Errorf("stat %q: %w", path, err)
	}

	if err := os.RemoveAll(absResolved); err != nil {
		return fmt.Errorf("remove %q: %w", path, err)
	}

	return nil
}

func (b *EmbeddedBackend) Bash(ctx context.Context, command, cwd string, limits BashLimits) (BashResult, error) {
	return b.runBash(ctx, command, cwd, nil, limits)
}

// BashStream executes a command and calls onLine for each batch of output
// lines during execution. If onLine is nil, behaves identically to Bash.
// Throttles callbacks to at most once per 10 lines or 2s, whichever comes
// first, to avoid flooding the bus with per-line events.
func (b *EmbeddedBackend) BashStream(ctx context.Context, command, cwd string, onLine func(line string), limits BashLimits) (BashResult, error) {
	return b.runBash(ctx, command, cwd, onLine, limits)
}

// runBash is the shared execution core for Bash and BashStream. It runs
// `sh -c command` with the whole process in its own process group (so a kill
// reaches every descendant, not just the shell), captures each stream through
// a bounded head+tail buffer (the middle is dropped, never held in memory),
// and kills the group when the combined output exceeds limits (bytes/lines)
// or the context is cancelled. Oversized output is truncated to head+tail
// with the omitted middle marked inline.
func (b *EmbeddedBackend) runBash(ctx context.Context, command, cwd string, onLine func(string), limits BashLimits) (BashResult, error) {
	if command == "" {
		return BashResult{}, fmt.Errorf("command is empty")
	}
	if cwd == "" {
		cwd = b.primary()
	} else {
		resolvedCwd, err := b.resolve(cwd)
		if err != nil {
			return BashResult{}, err
		}
		cwd = resolvedCwd
	}

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = cwd
	// Own process group: killing the group reaps every descendant, so a
	// backgrounded subprocess cannot outlive the command's deadline.
	setOwnProcessGroup(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return BashResult{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return BashResult{}, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return BashResult{}, fmt.Errorf("start: %w", err)
	}

	out := newBoundedWriter(limits)
	errOut := newBoundedWriter(limits)

	lineCh := make(chan string, 256)
	var overflow atomic.Bool

	var wg sync.WaitGroup
	wg.Add(2)
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()

	// readStream copies a pipe into a bounded writer, forwards complete lines
	// to lineCh for live streaming, and raises overflow when the combined
	// output breaches the call's limits. Non-blocking sends keep a killed
	// command from wedging readers on a full channel (the bounded writer
	// already captured the data).
	readStream := func(r io.Reader, w *boundedWriter, pending *[]byte) {
		defer wg.Done()
		buf := make([]byte, readChunkSize)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				if onLine != nil {
					forwardLines(pending, buf[:n], func(s string) {
						select {
						case lineCh <- s:
						default:
						}
					})
				}
				if exceededLimits(out, errOut, limits) {
					overflow.Store(true)
				}
			}
			if err != nil {
				break
			}
		}
		if onLine != nil && len(*pending) > 0 {
			select {
			case lineCh <- string(*pending):
			default:
			}
		}
	}

	go readStream(stdoutPipe, out, new([]byte))
	go readStream(stderrPipe, errOut, new([]byte))

	var batch []string
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if onLine != nil {
			onLine(strings.Join(batch, "\n"))
		}
		batch = batch[:0]
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	const batchSize = 10
	truncated := false
loop:
	for {
		if overflow.Load() {
			truncated = true
			flush()
			killGroup(cmd)
			break loop
		}
		select {
		case c := <-lineCh:
			batch = append(batch, c)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			flush()
			killGroup(cmd)
			_ = cmd.Wait()
			return BashResult{}, ctx.Err()
		case <-doneCh:
			for {
				select {
				case c := <-lineCh:
					batch = append(batch, c)
				default:
					flush()
					break loop
				}
			}
		}
	}

	// A fast command can finish (readers done) before the loop noticed the
	// overflow: the bounded capture still dropped the middle, so mark the
	// result as truncated. Nothing to kill there — the process already exited.
	if overflow.Load() {
		truncated = true
	}

	// Readers finish as the pipes close after the kill; only then are the
	// bounded writers stable to assemble.
	wg.Wait()
	err = cmd.Wait()
	result := BashResult{
		Stdout:     out.String(),
		Stderr:     errOut.String(),
		Truncated:  truncated,
		TotalBytes: int(out.total.Load() + errOut.total.Load()),
		TotalLines: int(out.lines.Load() + errOut.lines.Load()),
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return BashResult{}, fmt.Errorf("exec: %w", err)
	}
	return result, nil
}

// exceededLimits reports whether the combined output has breached the call's
// byte or line limits.
func exceededLimits(out, errOut *boundedWriter, limits BashLimits) bool {
	if limits.MaxBytes > 0 && out.total.Load()+errOut.total.Load() > int64(limits.MaxBytes) {
		return true
	}
	if limits.MaxLines > 0 && out.lines.Load()+errOut.lines.Load() > int64(limits.MaxLines) {
		return true
	}
	return false
}

// Grep runs `grep -rn` inside the primary mount (or the given path within
// any mount) and returns truncated results. Implements [GrepOperator].
func (b *EmbeddedBackend) Grep(ctx context.Context, pattern, path, include, exclude string) (string, error) {
	searchPath := path
	if searchPath == "" {
		searchPath = b.primary()
	} else {
		resolved, err := b.resolve(searchPath)
		if err != nil {
			return "", err
		}
		searchPath = resolved
	}

	cmdStr := fmt.Sprintf("grep -rn %s %s", ShellQuote(pattern), ShellQuote(searchPath))
	if include != "" {
		cmdStr += fmt.Sprintf(" --include=%s", ShellQuote(include))
	}
	if exclude != "" {
		cmdStr += fmt.Sprintf(" --exclude=%s", ShellQuote(exclude))
	}

	result, err := b.Bash(ctx, cmdStr, searchPath, BashLimits{MaxBytes: maxGrepBytes, MaxLines: maxGrepLines})
	if err != nil {
		return "", err
	}
	if result.ExitCode > 1 {
		return "", fmt.Errorf("grep failed (exit %d): %s", result.ExitCode, result.Stderr)
	}
	output := result.Stdout
	if output == "" {
		if result.ExitCode == 1 {
			return "no matches found", nil
		}
		return "(empty result)", nil
	}
	return output, nil
}

// Find runs `find -name` inside the primary mount (or the given path within
// any mount) and returns truncated results. Implements [FindOperator].
func (b *EmbeddedBackend) Find(ctx context.Context, path, pattern string) (string, error) {
	searchPath := path
	if searchPath == "" {
		searchPath = b.primary()
	} else {
		resolved, err := b.resolve(searchPath)
		if err != nil {
			return "", err
		}
		searchPath = resolved
	}

	cmdStr := fmt.Sprintf("find %s -name %s", ShellQuote(searchPath), ShellQuote(pattern))
	result, err := b.Bash(ctx, cmdStr, searchPath, BashLimits{MaxBytes: maxGrepBytes, MaxLines: maxGrepLines})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 && result.Stderr != "" {
		return "", fmt.Errorf("find failed: %s", result.Stderr)
	}
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		return "no files found", nil
	}
	return output, nil
}

// ── bounded output capture ──

const (
	// readChunkSize is the raw-bytes read granularity for command pipes.
	readChunkSize = 32 * 1024
	// maxForwardLine caps how much of a single unterminated line is buffered
	// for live streaming; longer lines are forwarded in pieces.
	maxForwardLine = 64 * 1024
)

// boundedWriter captures a stream's head and tail within a byte budget and
// counts bytes/lines. It implements io.Writer and never retains the full
// stream: the middle is dropped as it streams. With an unlimited budget it
// falls back to a plain accumulating buffer.
type boundedWriter struct {
	head []byte // first headLimit bytes
	tail []byte // last tailLimit bytes (ring)

	headLimit int
	tailLimit int
	total     atomic.Int64
	lines     atomic.Int64
	unlimited bool
	full      bytes.Buffer
}

// newBoundedWriter creates a writer that retains the head (first quarter) and
// tail (last three quarters) of a stream within limits.MaxBytes bytes. A
// non-positive MaxBytes keeps the full stream.
func newBoundedWriter(limits BashLimits) *boundedWriter {
	if limits.MaxBytes <= 0 {
		return &boundedWriter{unlimited: true}
	}
	w := &boundedWriter{}
	w.headLimit = limits.MaxBytes / 4
	w.tailLimit = limits.MaxBytes - w.headLimit
	return w
}

// Write appends p to the captured head and tail, counting bytes and lines.
// Only the owning reader goroutine calls Write, so head/tail need no lock;
// total/lines are atomic for the overflow checks in other goroutines.
func (w *boundedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	w.total.Add(int64(n))
	w.lines.Add(int64(bytes.Count(p, []byte{'\n'})))
	if w.unlimited {
		w.full.Write(p)
		return n, nil
	}
	if len(w.head) < w.headLimit {
		take := w.headLimit - len(w.head)
		if take > n {
			take = n
		}
		w.head = append(w.head, p[:take]...)
	}
	switch {
	case n >= w.tailLimit:
		w.tail = append(w.tail[:0], p[n-w.tailLimit:]...)
	case len(w.tail)+n > w.tailLimit:
		drop := len(w.tail) + n - w.tailLimit
		copy(w.tail, w.tail[drop:])
		w.tail = w.tail[:len(w.tail)-drop]
		w.tail = append(w.tail, p...)
	default:
		w.tail = append(w.tail, p...)
	}
	return n, nil
}

// String reassembles the captured stream. When the total fits the head+tail
// budget the full stream is reconstructed; otherwise head + omission marker +
// tail are returned. Call only after the reader goroutine has finished.
func (w *boundedWriter) String() string {
	if w.unlimited {
		return w.full.String()
	}
	t, h, l := w.total.Load(), int64(len(w.head)), int64(len(w.tail))
	switch {
	case t == 0:
		return ""
	case t <= h:
		return string(w.head[:t])
	case t <= h+l:
		overlap := h + l - t
		return string(w.head) + string(w.tail[overlap:])
	default:
		return string(w.head) + fmt.Sprintf("\n... [%d bytes omitted] ...\n", t-h-l) + string(w.tail)
	}
}

// forwardLines splits a raw output chunk into lines and forwards each
// complete line to onLine, buffering partial lines in pending. A line longer
// than maxForwardLine is forwarded in pieces so live streaming never buffers
// unboundedly.
func forwardLines(pending *[]byte, chunk []byte, onLine func(string)) {
	for {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			if len(chunk) > 0 {
				*pending = append(*pending, chunk...)
				if len(*pending) > maxForwardLine {
					onLine(string(*pending))
					*pending = nil
				}
			}
			return
		}
		*pending = append(*pending, chunk[:i]...)
		onLine(string(*pending))
		*pending = nil
		chunk = chunk[i+1:]
		if len(chunk) == 0 {
			return
		}
	}
}
