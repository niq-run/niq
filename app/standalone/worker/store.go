// Manifest storage boundary for the worker control plane. The project owns
// where manifests live and how they are written; the control plane only sees
// this interface. The concrete implementation is app/standalone/store
// (`store.NewManifest`), which the assembly injects.
//
// Runtime state (state.json / snapshots) is NOT declared here: its single
// definition is workerctl.WorkerStore, the contract the instance manager calls.
package worker

// ManifestStore is the manifest storage the control plane drives. It is the
// only place that knows WHERE manifests are written; the control plane only
// asks what is declared and records changes.
type ManifestStore interface {
	// Get returns one manifest. ok is false when the id is not declared.
	Get(id string) (*Manifest, bool, error)
	// List returns manifests: archived=false is the active set, archived=true
	// the archived set. Both are returned in declaration order.
	List(archived bool) ([]*Manifest, error)
	// Add records a new manifest. Declaring an existing id is an error: the
	// control plane checks for duplicates so it can report them precisely.
	Add(m *Manifest) error
	// Update replaces an existing manifest; an unknown id is an error.
	Update(m *Manifest) error
	// Delete removes a manifest from either set; an unknown id is a no-op.
	Delete(id string) error
	// Archive moves a manifest between the active and archived files. An
	// archived manifest is kept verbatim (it can be restored unchanged); a
	// missing id in the source file is a no-op.
	Archive(id string, archived bool) error
}
