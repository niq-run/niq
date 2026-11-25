package clipboard

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// nativeCopy is a best-effort clipboard write through the platform's own tool.
// OSC 52 covers terminals that implement it, but several (macOS Terminal,
// tmux without its clipboard option) ignore it, so the common helpers are tried
// too. Failures are silent: the copy is a convenience, not a command.
func Copy(text string) {
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "linux", "freebsd", "openbsd", "netbsd":
		candidates = [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "-ib"},
		}
	default:
		return
	}
	for _, argv := range candidates {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin = strings.NewReader(text)
		err := cmd.Run()
		cancel()
		if err == nil {
			return
		}
	}
}
