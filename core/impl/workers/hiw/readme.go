package hiw

import (
	"fmt"
)

// registerReadme declares the generic worker.readme capability. HIW has no
// general-purpose callable tools — it is the human's voice and the default
// approver — so the guide explains the role and the approval flow.
func (w *Worker) registerReadme() {
	w.RegisterReadme(
		"Read this worker's usage guide: the human presence, the approval flow it "+
			"owns, and the events it publishes.",
		w.readmeText, nil)
}

// readmeText is the guide plus the live approval-queue counts.
func (w *Worker) readmeText() string {
	w.approvalsMu.Lock()
	pending := len(w.pendingApprovals)
	decided := len(w.decidedApprovals)
	w.approvalsMu.Unlock()
	return fmt.Sprintf(
		"%s\n\n## Current\n\n- pending approvals: %d\n- decided approvals kept: %d\n",
		hiwReadme, pending, decided)
}

// hiwReadme is the agent-facing usage guide returned by worker.readme.
const hiwReadme = `# hiw — the human on the bus

This worker is the human's presence on the bus and, by default, the approver
workers ask before expanding their boundary. It is not a conversational worker
and exposes no general-purpose tools.

## Callable capability

- worker.readme {} — this guide.

## What it does

- Publishes human input as worker.input events (the WebUI sends through it).
- Tracks approval.request events addressed to it: when a workspace worker needs
  to mount a new directory or escape its boundary, it sends approval.request
  here. The WebUI reads the pending queue and sends the human's verdict back
  through HIW, which publishes approval.decision to the requester.
- Publishes arbitrary events on behalf of the human UI (an event a worker
  declared in its worker.ready "watch"), carrying the event id as the request
  id so the reply can be correlated.

## Approval flow

1. A worker sends approval.request with the action, target path and boundary
   context.
2. HIW parks it (durable across restarts) and exposes it to the UI.
3. The human decides; HIW removes it from pending, keeps a capped decided
   history, and sends approval.decision back to the requester with the same
   request id.

An approval.request is state, not a tool call: it has no request.completed
reply. The requester waits for approval.decision.

## Notes

- The UI drives HIW through its Go API (SendInput / SendEvent /
  SendApprovalDecision); those are not bus-callable events.
- HIW subscribes to nothing. Everything it receives is directed to it.`
