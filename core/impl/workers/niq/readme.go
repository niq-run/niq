package niq

import "fmt"

// readmeText is the guide plus the live link state and recipient bindings.
func (b *Backend) readmeText() string {
	local, remote := b.bindings()
	tgt := b.currentTarget()
	connected := b.remoteNow() != nil

	return fmt.Sprintf(
		"%s\n\n## Current\n\n- connected to the far-side bus: %t\n- far-side bus: %s\n- far-side identity: %s\n- far-side credential: %s\n- local recipient: %s\n- remote recipient: %s\n",
		readme,
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

// readme is the agent-facing usage guide returned by worker.readme.
const readme = `# niq — a link to another niq instance

This worker's backend is another niq instance. On this bus it is an ordinary
managed worker; at start it dials the far-side bus as a remote worker,
presenting the identity and credential that side issued to it. There it is just
a remote worker — the far side neither knows nor needs to know this worker type.

## Routing

- local → far side: a local worker.input is DIRECTED to the far side's "remote
  recipient" (the far side chooses which of its workers receives your input).
- far side → local: far-side worker.input is DIRECTED to the local "local
  recipient" (this side chooses which local worker receives those messages).
- Both directions are directed sends, never broadcasts. RequestId and TraceId
  are preserved in both directions.
- Far-side lifecycle and bookkeeping (worker.ready, worker.gone,
  worker.discover) is not relayed as conversation.

## Tools (on the local bus)

- niq.recipient.set { worker_id } — bind the LOCAL recipient for this channel.
  The same event arriving from the far side binds the REMOTE recipient instead:
  the channel decides which binding it mutates.
- niq.recipient.unset {} — clear this channel's recipient binding.
- niq.recipient.get {} — the current local and remote recipient bindings.
- niq.dial { remote_url?, remote_worker_id?, remote_credential? } — actively
  (re)connect the link, optionally repointing the far-side target. A repoint is
  persistent: the changed keys are merged into this worker's config, so a
  restart dials it again. Pass the credential as a reference ("env:NAME" /
  "file:PATH") to keep the token out of the project files; it stays a reference
  when saved.
- niq.status {} — live link status: connected, far-side target, bindings.

## Notes

- A repoint through niq.dial survives a restart. If it cannot be saved, the call
  fails and the old target stays in force: memory and config never disagree.
- A local worker.input is dropped while the far side has not set the remote
  recipient; set it there with niq.recipient.set, or seed it via config.
- The far side's PublishAllow for this remote identity is the security boundary:
  it decides which far-side targets this link may directed-send to.
- The far-side credential is never rendered here; check niq.status for whether
  it is configured.`
