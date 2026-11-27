// Package secret is an ASSEMBLY-LAYER responsibility: resolving the references
// a worker's params may carry (`env:NAME`, `file:/path`) into concrete values.
//
// It lives here, next to the code that builds workers, because that is who owns
// the question. A worker never sees a provider — it receives plain values (or,
// where it must resolve at runtime, a bare func). Keeping the interface out of
// core is deliberate: how a deployment stores secrets is not part of the worker
// contract, and a different form (a server, an embedded host) can wire a
// different provider without changing any worker.
//
// A reference is a scheme plus a locator. A value matching no known scheme is a
// literal and is used as-is, which is what keeps existing declarations working.
// The reference is what belongs in a worker's params; the value stays wherever
// the provider keeps it — params are written to workers.json in plain text.
package secret

// Provider resolves a reference to the value it names, and stores a value under
// a writable reference.
//
// Read and write are separate capabilities on purpose: `env:` is read-only (the
// environment is not a store we own), while `file:` and a future `vault:` are
// both. Store is what lets a caller receive a credential out of band and persist
// only the reference.
type Provider interface {
	// Resolve expands a reference. An unrecognized scheme is a literal: the
	// argument is returned unchanged.
	Resolve(ref string) string
	// Store writes value at ref. Schemes that cannot be written (env:) or are
	// unknown return an error.
	Store(ref, value string) error
}
