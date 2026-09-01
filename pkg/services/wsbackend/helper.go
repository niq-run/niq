package wsbackend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxReadLines = 2000
	maxReadBytes = 50_000
	maxGrepLines = 10_000
	maxGrepBytes = 1_000_000
)

// MaxBashBytes / MaxBashLines cap a single bash tool result (stdout+stderr
// combined). Oversized output is truncated to head+tail and the process is
// killed so a runaway command cannot flood the event bus or leave orphans.
const (
	MaxBashBytes = 20 * 1024
	MaxBashLines = 2000
)

// FormatRead formats file contents with line numbering. The backend
// handles offset/limit slicing before this is called — this function
// only adds line numbers and header.
func FormatRead(path, content string, args map[string]any) string {
	if content == "" {
		return path + " (empty)"
	}
	lines := strings.Split(content, "\n")
	offset := GetIntArg(args, "offset", 1)
	if offset < 1 {
		offset = 1
	}
	limit := GetIntArg(args, "limit", 0)

	shown := len(lines)
	totalBytes := len(content)

	truncated := false
	if limit > 0 && shown >= limit {
		truncated = true
	}
	if shown > maxReadLines {
		truncated = true
	}
	if totalBytes > maxReadBytes {
		truncated = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d lines, %d bytes)", path, shown, totalBytes)
	if truncated {
		fmt.Fprintf(&b, " — lines %d-%d (truncated)", offset, offset+shown-1)
	}
	b.WriteString("\n")

	for i, line := range lines {
		fmt.Fprintf(&b, "%6d\t%s\n", offset+i, line)
	}

	return strings.TrimRight(b.String(), "\n")
}

// FormatBash formats a command result (exit code, stdout, stderr) for
// LLM consumption. When the result was truncated, a guidance note is
// appended so the model knows how to narrow the output.
func FormatBash(r BashResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Exit code: %d", r.ExitCode)
	if r.Stdout != "" {
		b.WriteString("\nSTDOUT:\n")
		b.WriteString(strings.TrimRight(r.Stdout, "\n"))
	}
	if r.Stderr != "" {
		b.WriteString("\nSTDERR:\n")
		b.WriteString(strings.TrimRight(r.Stderr, "\n"))
	}
	if r.Truncated {
		fmt.Fprintf(&b, "\n[Output truncated: total %d bytes. "+
			"Use head/tail/grep/filter to narrow output, or redirect to file then use read_file.]", r.TotalBytes)
	}
	return b.String()
}

// FormatLs formats a directory listing with [dir] / [file] tags and a
// summary line.
func FormatLs(entries []DirEntry) string {
	if len(entries) == 0 {
		return "(empty directory)"
	}
	var b strings.Builder
	dirCount, fileCount := 0, 0
	for _, e := range entries {
		if e.IsDir {
			fmt.Fprintf(&b, "[dir]  %s\n", e.Name)
			dirCount++
		} else {
			fmt.Fprintf(&b, "[file] %s\n", e.Name)
			fileCount++
		}
	}
	fmt.Fprintf(&b, "\n%d files, %d directories", fileCount, dirCount)
	return b.String()
}

// NormalizeQuotes replaces Unicode curly quotes and dashes with their
// ASCII equivalents. Used by the edit fuzzy fallback.
func NormalizeQuotes(s string) string {
	return quoteMap.Replace(s)
}

var quoteMap = strings.NewReplacer(
	"\u201c", `"`, // "
	"\u201d", `"`, // "
	"\u2018", "'", // '
	"\u2019", "'", // '
	"\u2013", "--", // –
	"\u2014", "--", // —
	"\u00ab", `"`, // «
	"\u00bb", `"`, // »
)

// ShellQuote wraps a string in single quotes for safe shell usage.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	q := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + q + "'"
}

// TruncateOutput truncates text to maxLines and/or maxBytes, appending
// a note if truncated.
func TruncateOutput(output string, maxLines, maxBytes int) string {
	lines := strings.Split(output, "\n")
	truncated := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}

	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	result := b.String()

	if len(result) > maxBytes {
		for maxBytes > 0 && !utf8.RuneStart(result[maxBytes]) {
			maxBytes--
		}
		result = result[:maxBytes]
		truncated = true
	}

	result = strings.TrimRight(result, "\n")
	if truncated {
		result += "\n... (truncated)"
	}
	return result
}

// GetIntArg extracts an integer argument from a tool args map, returning
// defaultVal if the key is absent or unparseable.
func GetIntArg(args map[string]any, key string, defaultVal int) int {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return defaultVal
}

// resolvePath cleans and absolutises a path, then checks that it stays
// within rootDir. Returns the resolved path or an error.
// expandHome expands a leading ~ in path to the user's home directory.
func expandHome(path string) (string, error) {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func resolvePath(rootDir, raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("path is empty")
	}

	// Expand leading ~ to the user home directory.
	if raw == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		raw = home
	} else if strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		raw = filepath.Join(home, raw[2:])
	}

	cleaned := filepath.Clean(raw)
	var abs string
	var err error
	if filepath.IsAbs(cleaned) {
		abs, err = filepath.Abs(cleaned)
	} else {
		abs, err = filepath.Abs(filepath.Join(rootDir, cleaned))
	}
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	// Symlink check: resolve symlinks and verify containment.
	// If EvalSymlinks fails (path does not exist yet — common for
	// writes), fall back to the basic Abs prefix check.
	real, evalErr := filepath.EvalSymlinks(abs)
	if evalErr != nil {
		if !strings.HasPrefix(abs, rootDir) {
			return "", fmt.Errorf("path escapes workspace: %s", raw)
		}
		return abs, nil
	}

	realRoot, _ := filepath.EvalSymlinks(rootDir)
	if realRoot == "" {
		realRoot = rootDir
	}
	if !strings.HasPrefix(real, realRoot) {
		return "", fmt.Errorf("path escapes workspace: %s", raw)
	}
	return abs, nil
}
