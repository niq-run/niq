// Worker roster and routing helpers: which workers a terminal can address, how a
// message's target is chosen and named, and how an event's addressed worker is
// read back out of the bus envelope. These are pure functions over
// contract.Worker / contract.Event, kept apart from the event → block rendering
// in render.go so the renderer reads as shape, not roster bookkeeping.
package tui

import (
	"strings"

	"github.com/niq-run/niq/app/standalone/contract"
)

// talkPartnerTypes are the worker types a terminal can converse with — the ones
// that consume worker.input and answer in the conversation. This mirrors the
// WebUI's TALK_PARTNER_TYPES so both interfaces agree on who is addressable.
var talkPartnerTypes = map[string]bool{"reason": true, "coding": true, "niq": true}

// talkPartners keeps the workers a message can be addressed to, in listing order.
func talkPartners(workers []contract.Worker) []contract.Worker {
	out := make([]contract.Worker, 0, len(workers))
	for _, w := range workers {
		if talkPartnerTypes[w.Type] {
			out = append(out, w)
		}
	}
	return out
}

// pickTarget chooses the worker a message goes to by default: the first online
// partner, falling back to the first partner when every one of them is offline
// (a send then surfaces the error). The user can change it at any time with the
// target selector.
func pickTarget(partners []contract.Worker) string {
	for _, w := range partners {
		if w.Online {
			return w.ID
		}
	}
	if len(partners) > 0 {
		return partners[0].ID
	}
	return ""
}

// workerName is a partner's display name: its label when set, else its id.
func workerName(w contract.Worker) string {
	if w.Label != "" {
		return w.Label
	}
	return w.ID
}

// indexOfWorker returns the index of id in ws, or 0 when it is not listed.
func indexOfWorker(ws []contract.Worker, id string) int {
	for i, w := range ws {
		if w.ID == id {
			return i
		}
	}
	return 0
}

// toolTarget is whom a call addressed: the single target, or the routed
// recipients when the bus only recorded those.
func toolTarget(ev contract.Event) string {
	if ev.TargetWorkerID != "" {
		return ev.TargetWorkerID
	}
	return strings.Join(ev.Recipients, ", ")
}

// inputRecipients is whom the bus recorded a worker.input as addressed to: the
// routed Recipients slice, or the single-target field on older events.
func inputRecipients(ev contract.Event) []string {
	if len(ev.Recipients) > 0 {
		return ev.Recipients
	}
	if ev.TargetWorkerID != "" {
		return []string{ev.TargetWorkerID}
	}
	return nil
}

// matchesFilter reports whether an event belongs in the transcript when the view
// is scoped to one worker. An empty filter means everything. Otherwise the event
// counts if that worker sent it, was its single target, or was among the routed
// recipients — the same envelope rule the WebUI's talk scope uses.
func matchesFilter(ev contract.Event, filter string) bool {
	if filter == "" {
		return true
	}
	if ev.WorkerID == filter || ev.TargetWorkerID == filter {
		return true
	}
	for _, r := range ev.Recipients {
		if r == filter {
			return true
		}
	}
	return false
}

// workerLabel is the speaker name for the left side of a conversation block.
func workerLabel(id string) string {
	if id == "" {
		return "worker"
	}
	return id
}
