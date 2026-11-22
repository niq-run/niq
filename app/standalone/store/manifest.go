// Package store implements the standalone form's durable storage: worker
// manifests (workers.json / workers.archived.json), runtime state
// (workers/<id>/state.json), bus identities (identity/), the SQLite event
// store (event/) and the one-time migration of legacy files (migrate/).
//
// Everything here is path-based: the caller passes the project directory or the
// state root, so this package knows no project ids and no ~/.niq layout. It is
// also the only layer that knows the on-disk shapes.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/niq-run/niq/app/standalone/store/migrate"
	projworker "github.com/niq-run/niq/app/standalone/worker"
)

// SchemaVersion is the on-disk worker-file format this package reads and
// writes. Legacy projects are converted once by migrate before they reach here.
const SchemaVersion = 2

// WorkerSet is the on-disk shape of both workers.json and
// workers.archived.json: the filename is the only difference.
type WorkerSet struct {
	SchemaVersion int                   `json:"schema_version"`
	Workers       []projworker.Manifest `json:"workers"`
}

// WorkersPath returns the active worker-manifest file in dir.
func WorkersPath(dir string) string { return filepath.Join(dir, "workers.json") }

// ArchivedPath returns the archived worker-manifest file in dir.
func ArchivedPath(dir string) string { return filepath.Join(dir, "workers.archived.json") }

// ensureProject runs the one-time conversion before a read. A store that read a
// legacy file directly would silently parse it as the current schema.
func ensureProject(dir string) error { return migrate.EnsureProject(dir) }

// LoadWorkers loads the active manifest set. A missing file is an empty set (a
// project may legitimately have no workers).
func LoadWorkers(dir string) (*WorkerSet, error) {
	if err := ensureProject(dir); err != nil {
		return nil, err
	}
	return loadSetFile(WorkersPath(dir))
}

// LoadArchived loads the archived manifest set.
func LoadArchived(dir string) (*WorkerSet, error) {
	if err := ensureProject(dir); err != nil {
		return nil, err
	}
	return loadSetFile(ArchivedPath(dir))
}

// SaveWorkers writes the active manifest set.
func SaveWorkers(dir string, set *WorkerSet) error { return saveSet(dir, set, false) }

// SaveArchived writes the archived manifest set.
func SaveArchived(dir string, set *WorkerSet) error { return saveSet(dir, set, true) }

// FindWorker returns a manifest from a set by id.
func FindWorker(set *WorkerSet, id string) (projworker.Manifest, bool) {
	if set == nil {
		return projworker.Manifest{}, false
	}
	for _, m := range set.Workers {
		if m.ID == id {
			return m, true
		}
	}
	return projworker.Manifest{}, false
}

// ArchivedIDs returns the ids held in the archived file, sorted.
func ArchivedIDs(dir string) ([]string, error) {
	set, err := LoadArchived(dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set.Workers))
	for i := range set.Workers {
		out = append(out, set.Workers[i].ID)
	}
	sort.Strings(out)
	return out, nil
}

// manifestWrites serializes read-modify-write cycles on a project's manifest
// files. Every writer goes through it — the control plane's store, the WebUI's
// edits (which arrive as host tool calls), an archive move — so two concurrent
// writers cannot silently revert each other. Reads take no lock: they need a
// consistent file, which the atomic rename gives them.
var manifestWrites sync.Mutex

func withManifestWrite(fn func() error) error {
	manifestWrites.Lock()
	defer manifestWrites.Unlock()
	return fn()
}

// SetArchived moves a manifest between the active and archived files.
//
// The destination is written first and the source second: a crash in between
// leaves the manifest in BOTH files (visible, and fixed by the next archive or
// unarchive) rather than in neither. Archiving is not a delete — the manifest is
// preserved verbatim so unarchiving restores the worker as it was.
func SetArchived(dir, id string, archived bool) error {
	return withManifestWrite(func() error {
		// Archiving moves active → archived; unarchiving moves archived → active.
		srcArchived, dstArchived := !archived, archived
		src, err := loadSet(dir, srcArchived)
		if err != nil {
			return err
		}
		var moved *projworker.Manifest
		kept := src.Workers[:0]
		for i := range src.Workers {
			if src.Workers[i].ID == id {
				m := src.Workers[i]
				moved = &m
				continue
			}
			kept = append(kept, src.Workers[i])
		}
		if moved == nil {
			// Already on the requested side, or unknown: either way there is
			// nothing to move.
			return nil
		}

		dst, err := loadSet(dir, dstArchived)
		if err != nil {
			return err
		}
		if _, ok := FindWorker(dst, id); !ok {
			dst.Workers = append(dst.Workers, *moved)
			if err := saveSet(dir, dst, dstArchived); err != nil {
				return err
			}
		}
		src.Workers = kept
		return saveSet(dir, src, srcArchived)
	})
}

// Remove removes a manifest from both files.
func Remove(dir, id string) error {
	return withManifestWrite(func() error {
		for _, archived := range []bool{false, true} {
			set, err := loadSet(dir, archived)
			if err != nil {
				return err
			}
			out := set.Workers[:0]
			changed := false
			for _, m := range set.Workers {
				if m.ID == id {
					changed = true
					continue
				}
				out = append(out, m)
			}
			if changed {
				set.Workers = out
				if err := saveSet(dir, set, archived); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Manifest implements worker.ManifestStore over one project directory.
type Manifest struct {
	dir string
}

// NewManifest creates the manifest store for a project directory.
func NewManifest(dir string) *Manifest { return &Manifest{dir: dir} }

// Get returns one active manifest.
func (m *Manifest) Get(id string) (*projworker.Manifest, bool, error) {
	set, err := LoadWorkers(m.dir)
	if err != nil {
		return nil, false, err
	}
	d, ok := FindWorker(set, id)
	if !ok {
		return nil, false, nil
	}
	return &d, true, nil
}

// List returns the active or archived set.
func (m *Manifest) List(archived bool) ([]*projworker.Manifest, error) {
	set, err := loadSet(m.dir, archived)
	if err != nil {
		return nil, err
	}
	out := make([]*projworker.Manifest, 0, len(set.Workers))
	for i := range set.Workers {
		d := set.Workers[i]
		out = append(out, &d)
	}
	return out, nil
}

// Add records a new manifest in the active set.
func (m *Manifest) Add(d *projworker.Manifest) error {
	set, err := LoadWorkers(m.dir)
	if err != nil {
		return err
	}
	if _, ok := FindWorker(set, d.ID); ok {
		return fmt.Errorf("worker %s already exists", d.ID)
	}
	set.Workers = append(set.Workers, *d)
	return SaveWorkers(m.dir, set)
}

// Update replaces an existing active manifest.
func (m *Manifest) Update(d *projworker.Manifest) error {
	set, err := LoadWorkers(m.dir)
	if err != nil {
		return err
	}
	for i := range set.Workers {
		if set.Workers[i].ID == d.ID {
			set.Workers[i] = *d
			return SaveWorkers(m.dir, set)
		}
	}
	return fmt.Errorf("worker %s is not declared", d.ID)
}

// Delete removes a manifest from either set.
func (m *Manifest) Delete(id string) error { return Remove(m.dir, id) }

// Archive moves a manifest between the active and archived files.
func (m *Manifest) Archive(id string, archived bool) error {
	return SetArchived(m.dir, id, archived)
}

// loadSet loads one of the two sets, running the migration first.
func loadSet(dir string, archived bool) (*WorkerSet, error) {
	if err := ensureProject(dir); err != nil {
		return nil, err
	}
	path := WorkersPath(dir)
	if archived {
		path = ArchivedPath(dir)
	}
	return loadSetFile(path)
}

func loadSetFile(path string) (*WorkerSet, error) {
	set := &WorkerSet{SchemaVersion: SchemaVersion}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return set, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, set); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", path, err)
	}
	if set.SchemaVersion == 0 {
		set.SchemaVersion = SchemaVersion
	}
	if set.Workers == nil {
		set.Workers = []projworker.Manifest{}
	}
	return set, nil
}

func saveSet(dir string, set *WorkerSet, archived bool) error {
	if set.SchemaVersion == 0 {
		set.SchemaVersion = SchemaVersion
	}
	if set.Workers == nil {
		set.Workers = []projworker.Manifest{}
	}
	path := WorkersPath(dir)
	if archived {
		path = ArchivedPath(dir)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("store: mkdir: %w", err)
	}
	return writeJSONAtomic(path, set)
}

// writeJSONAtomic writes v as JSON through a temp file + rename, so a crash
// never leaves a half-written manifest file.
func writeJSONAtomic(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal %s: %w", path, err)
	}
	raw = append(raw, '\n')
	return writeRawAtomic(path, raw)
}

// writeRawAtomic writes bytes through a temp file + rename.
func writeRawAtomic(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return fmt.Errorf("store: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("store: rename %s: %w", path, err)
	}
	return nil
}
