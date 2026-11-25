// The live event stream pump: keep a subscription open, reconnect on a dropped
// connection with a capped backoff, and report state to the view.
package live

import (
	"context"
	"time"

	"github.com/niq-run/niq/app/standalone/contract"
)

// ── Live stream ──

// follow keeps a live subscription open, posting the watermark on each connect
// and every event as it arrives. A stopped or restarting project answers with an
// error rather than hanging, which is what the backoff is for.
func Follow(ctx context.Context, client *contract.Client, projectID string, events chan<- contract.Event, opened chan<- string, notes chan<- string) {
	backoff := time.Second
	const maxBackoff = 5 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		stream, err := client.Follow(ctx, projectID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			notify(ctx, notes, "stream: "+err.Error())
			if !sleep(ctx, backoff) {
				return
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if wm := stream.Watermark(); wm != "" {
			select {
			case opened <- wm:
			case <-ctx.Done():
				stream.Close()
				return
			}
		}
		for {
			ev, err := stream.Next()
			if err != nil {
				stream.Close()
				if ctx.Err() != nil {
					return
				}
				notify(ctx, notes, "stream dropped: "+err.Error())
				break
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				stream.Close()
				return
			}
		}
		if !sleep(ctx, backoff) {
			return
		}
	}
}

func notify(ctx context.Context, notes chan<- string, msg string) {
	select {
	case notes <- msg:
	case <-ctx.Done():
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
