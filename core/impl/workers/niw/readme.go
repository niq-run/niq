package niw

import "fmt"

// registerReadme declares the generic worker.readme capability: the guide
// describes the two-bus routing and the recipient controls, plus the live
// bindings. The remote credential is never rendered.
func (w *Worker) registerReadme() {
	w.RegisterReadme(
		"Read this worker's usage guide: how the two-bus bridge routes messages, "+
			"its recipient controls, and the current bindings.",
		w.readmeText, nil)
}

// readmeText is the guide plus the live link state and recipient bindings.
func (w *Worker) readmeText() string {
	local, remote := w.bindings()
	tgt := w.currentTarget()
	connected := w.remoteNow() != nil

	return fmt.Sprintf(
		"%s\n\n## Current\n\n- connected to the remote bus: %t\n- remote bus: %s\n- remote identity: %s\n- remote credential: %s\n- local recipient: %s\n- remote recipient: %s\n",
		niwReadme,
		connected,
		orUnset(tgt.url),
		orUnset(tgt.workerID),
		credentialState(tgt.cred),
		orUnset(local),
		orUnset(remote),
	)
}

func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

// credentialState says whether a credential is configured without revealing it:
// worker.readme is readable by peers, so the secret must never ride it.
func credentialState(cred string) string {
	if cred == "" {
		return "not configured"
	}
	return "configured (not shown)"
}

// niwReadme is the agent-facing usage guide returned by worker.readme.
const niwReadme = `# niw — bridge between two niq buses

This worker links this niq instance (A) to another niq instance (B). On A it is
an ordinary managed worker; at start it dials B as a remote worker, presenting
the identity and credential B issued to it. On B it is just a remote worker.

## Routing

- A → B: a local worker.input is DIRECTED to the far-side "remote recipient"
  (B chooses which of its workers receives A's input).
- B → A: far-side worker.input is DIRECTED to the local "local recipient"
  (A chooses which local worker receives B's messages).
- Both directions are directed sends, never broadcasts. RequestId and TraceId
  are preserved in both directions.
- Far-side lifecycle/bookkeeping (worker.ready, worker.gone, worker.discover)
  is not relayed as conversation.

## Tools (on the local bus)

- niw.recipient.set { worker_id } — bind the LOCAL recipient for this channel.
  The same event arriving from B binds B's remote recipient instead: the channel
  decides which binding it mutates.
- niw.recipient.unset {} — clear this channel's recipient binding.
- niw.recipient.get {} — the current local and remote recipient bindings.
- niw.dial { remote_url?, remote_worker_id?, remote_credential? } — actively
  (re)connect the bridge, optionally repointing the far-side target.
- niw.status {} — live bridge status: connected, remote target, bindings.

## Notes

- A local worker.input is dropped when B has not yet set the remote recipient;
  set it locally with niw.recipient.set on the far side, or via config.
- B's PublishAllow for this remote identity is the security boundary: it decides
  which local targets B may directed-send to.
- The remote credential is never rendered here; check niw.status for whether it
  is configured.`
