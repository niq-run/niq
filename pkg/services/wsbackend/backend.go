package wsbackend

import "context"

// CommandResult holds the structured output of a command execution.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// onUpdateKey is the context key for the streaming onUpdate callback.
type onUpdateKey struct{}

// WithOnUpdate returns a context with an onUpdate callback attached.
func WithOnUpdate(ctx context.Context, fn func(partial string)) context.Context {
	return context.WithValue(ctx, onUpdateKey{}, fn)
}

// OnUpdate retrieves the onUpdate callback from the context, or nil.
func OnUpdate(ctx context.Context) func(string) {
	fn, _ := ctx.Value(onUpdateKey{}).(func(string))
	return fn
}

// FileOperator provides read, write, and edit operations.
type FileOperator interface {
	Read(ctx context.Context, path string, offset, limit int) (string, error)
	Write(ctx context.Context, path, content string) error
	Edit(ctx context.Context, path, oldStr, newStr string) error
}

// DirEntry represents a single item in a directory listing.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

// DirLister provides directory listing operations.
type DirLister interface {
	List(ctx context.Context, path string) ([]DirEntry, error)
}

// BashLimits caps a single command execution. Zero values mean unlimited.
// MaxBytes/MaxLines apply to stdout+stderr combined; when either is exceeded
// the process group is killed and the captured output is truncated.
type BashLimits struct {
	MaxBytes int
	MaxLines int
}

// BashResult holds the structured output of a bash command.
type BashResult struct {
	Stdout   string
	Stderr   string
	ExitCode int

	// Truncated reports whether the output exceeded the call's limits: the
	// process group was killed and the captured streams are head+tail slices
	// of what was produced (the middle omitted, marked inline).
	Truncated  bool
	TotalBytes int
	TotalLines int
}

// BashOperator provides command execution.
type BashOperator interface {
	Bash(ctx context.Context, command, cwd string, limits BashLimits) (BashResult, error)

	// BashStream executes a command and calls onLine for each output
	// line during execution. Backends may throttle the callback rate.
	// When onLine is nil, behaves identically to Bash.
	BashStream(ctx context.Context, command, cwd string, onLine func(line string), limits BashLimits) (BashResult, error)
}

// GrepOperator provides recursive regex pattern search. Backends
// implement this with whatever native search mechanism is available
// (grep binary, ripgrep, daemon-side FS walk).
type GrepOperator interface {
	Grep(ctx context.Context, pattern, path, include, exclude string) (string, error)
}

// FindOperator provides filename glob search. Mirrors find -name.
type FindOperator interface {
	Find(ctx context.Context, path, pattern string) (string, error)
}
