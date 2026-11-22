//go:build !unix

package helper

import (
	"os"
	"time"
)

// Holders is unavailable without lsof; the caller can still offer "start on
// another port".
func Holders(port int) ([]Holder, error) { return nil, ErrUnsupported }

// Signal is a no-op where discovery is unavailable.
func Signal(port int, sig os.Signal) int { return 0 }

// Kill cannot stop a holder here.
func Kill(port int, grace time.Duration) (int, error) { return 0, ErrUnsupported }
