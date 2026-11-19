// Project storage: each project lives under <niq root>/projects/<id>/.
//
//	project.json            project metadata only (id / ports / created_at / upload_dir)
//	workers.json            active worker declarations
//	workers.archived.json   archived worker declarations (same shape)
//	root_secret             bus token signing secret
//	workers/<id>/           runtime state (state.json / stdout.log / process state)
//
// The split keeps startup proportional to the active set: archived declarations
// can number in the hundreds without being loaded or re-materialized.
package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/niq-run/niq/app/niqhome"
	"github.com/niq-run/niq/app/standalone/project/migrate"
	projtemplate "github.com/niq-run/niq/app/standalone/project/template"
	projworker "github.com/niq-run/niq/app/standalone/worker"
	"github.com/niq-run/niq/core/itfs/event"
)

// SchemaVersion is the on-disk format this package reads and writes. Legacy
// projects are converted once by app/project/migrate before they reach here.
const SchemaVersion = 2

// ProjectPorts records the ports assigned to a project instance: its event-bus
// (HTTP transport) port and its WebUI port. Zero means "not allocated".
type ProjectPorts struct {
	Bus   int `json:"bus,omitempty"`
	WebUI int `json:"webui,omitempty"`
}

// Project is the project-level metadata. Worker declarations live in
// workers.json / workers.archived.json, never here.
type Project struct {
	SchemaVersion int          `json:"schema_version,omitempty"`
	ID            string       `json:"id"`
	CreatedAt     string       `json:"created_at,omitempty"`
	Ports         ProjectPorts `json:"ports,omitempty"`
	UploadDir     string       `json:"upload_dir,omitempty"`
}

// WorkerSet is the on-disk shape of both workers.json and
// workers.archived.json: the filename is the only difference.
type WorkerSet struct {
	SchemaVersion int                      `json:"schema_version"`
	Workers       []projworker.Declaration `json:"workers"`
}

// ProjectsRoot returns <niq root>/projects (created lazily on write).
func ProjectsRoot() string { return filepath.Join(niqhome.Root(), "projects") }

// ProjectDir returns the directory for a project id.
func ProjectDir(id string) string { return filepath.Join(ProjectsRoot(), sanitizeID(id)) }

// ProjectPath returns the project.json path for a project id.
func ProjectPath(id string) string { return filepath.Join(ProjectDir(id), "project.json") }

// WorkersPath returns the active worker-declaration file for a project.
func WorkersPath(id string) string { return filepath.Join(ProjectDir(id), "workers.json") }

// ArchivedPath returns the archived worker-declaration file for a project.
func ArchivedPath(id string) string { return filepath.Join(ProjectDir(id), "workers.archived.json") }

// RootSecretPath returns the project's token-signing secret path.
func RootSecretPath(id string) string { return filepath.Join(ProjectDir(id), "root_secret") }

// LogPath is where a project process writes its log. The date in the filename
// gives free daily rotation: a new day starts a new file, same-day restarts
// append.
func LogPath(id string) string {
	return filepath.Join(ProjectDir(id), "logs", "niq-"+time.Now().Format("2006-01-02")+".log")
}

// CreateProject creates a project directory, its metadata file and its active
// worker file, seeding the template's declarations. It fails if the project id
// already exists.
func CreateProject(id, templateName string, template *TemplateConfig) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project: id is required")
	}
	if _, err := os.Lstat(ProjectPath(id)); err == nil {
		return nil, fmt.Errorf("project: %q already exists", id)
	}
	if err := os.MkdirAll(ProjectDir(id), 0755); err != nil {
		return nil, fmt.Errorf("project: mkdir: %w", err)
	}
	p := &Project{SchemaVersion: SchemaVersion, ID: id, CreatedAt: time.Now().Format(time.RFC3339)}
	if template != nil {
		p.UploadDir = template.UploadDir
	}
	if err := SaveProject(p); err != nil {
		return nil, err
	}
	set := &WorkerSet{SchemaVersion: SchemaVersion}
	if template != nil {
		for _, d := range template.Workers {
			set.Workers = append(set.Workers, projworker.Instantiate(d, id))
		}
	}
	if err := SaveWorkers(id, set); err != nil {
		return nil, err
	}
	if templateName != "" {
		if _, err := projtemplate.CopyPrograms(projtemplate.TemplatesDir(), templateName, ProjectProgramsDir(id)); err != nil {
			return nil, fmt.Errorf("project: copy template programs: %w", err)
		}
	}
	return p, nil
}

// FindWorker returns a declaration from a worker set by id.
func FindWorker(set *WorkerSet, id string) (projworker.Declaration, bool) {
	if set == nil {
		return projworker.Declaration{}, false
	}
	for _, d := range set.Workers {
		if d.ID == id {
			return d, true
		}
	}
	return projworker.Declaration{}, false
}

// declWrites serializes read-modify-write cycles on a project's declaration
// files. Every writer goes through it — the lifecycle's declaration store, the
// WebUI's edits (which arrive as host tool calls), an archive move, the archive
// cleanup — so two concurrent writers cannot silently revert each other. Reads
// take no lock: they need a consistent file, which the atomic rename gives them.
var declWrites sync.Mutex

// withDeclWrite runs fn under the declaration write lock.
func withDeclWrite(fn func() error) error {
	declWrites.Lock()
	defer declWrites.Unlock()
	return fn()
}

// SetWorkerArchived moves a declaration between the active and archived files.
//
// The destination is written first and the source second: a crash in between
// leaves the declaration in BOTH files (visible, and fixed by the next archive
// or unarchive) rather than in neither. Archiving is not a delete — the
// declaration is preserved verbatim so unarchiving restores the worker as it
// was.
func SetWorkerArchived(projectID, id string, archived bool) error {
	return withDeclWrite(func() error {
		// Archiving moves active → archived; unarchiving moves archived → active.
		srcArchived, dstArchived := !archived, archived
		src, err := loadSetFor(projectID, srcArchived)
		if err != nil {
			return err
		}
		var moved *projworker.Declaration
		kept := src.Workers[:0]
		for i := range src.Workers {
			if src.Workers[i].ID == id {
				d := src.Workers[i]
				moved = &d
				continue
			}
			kept = append(kept, src.Workers[i])
		}
		if moved == nil {
			// Already on the requested side, or unknown: either way there is
			// nothing to move.
			return nil
		}

		dst, err := loadSetFor(projectID, dstArchived)
		if err != nil {
			return err
		}
		if _, ok := FindWorker(dst, id); !ok {
			dst.Workers = append(dst.Workers, *moved)
			if err := saveSet(projectID, dst, dstArchived); err != nil {
				return err
			}
		}
		src.Workers = kept
		return saveSet(projectID, src, srcArchived)
	})
}

// ArchivedWorkerIDs returns the ids held in the archived file.
func ArchivedWorkerIDs(projectID string) ([]string, error) {
	set, err := LoadArchived(projectID)
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

// UpdateWorkerACL persists a worker's bus ACL into its declaration (active or
// archived). The runtime registry is a projection; workers.json is the source.
func UpdateWorkerACL(projectID, id string, publish []event.PublishPattern, subscribe []event.EventPattern) error {
	pubs := make([]projworker.PublishSpec, 0, len(publish))
	for _, p := range publish {
		pubs = append(pubs, projworker.PublishSpec{Type: p.Type, Target: p.Target})
	}
	subs := make([]projworker.SubscriptionSpec, 0, len(subscribe))
	for _, p := range subscribe {
		subs = append(subs, projworker.SubscriptionSpec{Type: p.Type, Source: p.SourceID})
	}
	return updateDecl(projectID, id, func(d *projworker.Declaration) {
		d.Publish = pubs
		d.Subscriptions = subs
	})
}

// updateDecl applies fn to a declaration in whichever worker file holds it.
func updateDecl(projectID, id string, fn func(*projworker.Declaration)) error {
	return withDeclWrite(func() error {
		for _, archived := range []bool{false, true} {
			set, err := loadSetFor(projectID, archived)
			if err != nil {
				return err
			}
			for i := range set.Workers {
				if set.Workers[i].ID == id {
					fn(&set.Workers[i])
					return saveSet(projectID, set, archived)
				}
			}
		}
		return fmt.Errorf("worker %s not found", id)
	})
}

func loadSetFor(projectID string, archived bool) (*WorkerSet, error) {
	path := WorkersPath(projectID)
	if archived {
		path = ArchivedPath(projectID)
	}
	if err := migrate.EnsureProject(ProjectDir(projectID)); err != nil {
		return nil, err
	}
	return loadSet(path)
}

// RemoveWorkerDecl removes a declaration from both worker files.
func RemoveWorkerDecl(projectID, id string) error {
	return withDeclWrite(func() error {
		for _, archived := range []bool{false, true} {
			set, err := loadSetFor(projectID, archived)
			if err != nil {
				return err
			}
			out := set.Workers[:0]
			changed := false
			for _, d := range set.Workers {
				if d.ID == id {
					changed = true
					continue
				}
				out = append(out, d)
			}
			if changed {
				set.Workers = out
				if err := saveSet(projectID, set, archived); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// LoadWorkers loads a project's active worker declarations. A missing file is
// an empty set (a project may legitimately have no workers).
func LoadWorkers(projectID string) (*WorkerSet, error) {
	if err := migrate.EnsureProject(ProjectDir(projectID)); err != nil {
		return nil, err
	}
	return loadSet(WorkersPath(projectID))
}

// SaveWorkers writes a project's active worker declarations.
func SaveWorkers(projectID string, set *WorkerSet) error { return saveSet(projectID, set, false) }

// LoadArchived loads a project's archived worker declarations.
func LoadArchived(projectID string) (*WorkerSet, error) {
	if err := migrate.EnsureProject(ProjectDir(projectID)); err != nil {
		return nil, err
	}
	return loadSet(ArchivedPath(projectID))
}

// SaveArchived writes a project's archived worker declarations.
func SaveArchived(projectID string, set *WorkerSet) error { return saveSet(projectID, set, true) }

func loadSet(path string) (*WorkerSet, error) {
	set := &WorkerSet{SchemaVersion: SchemaVersion}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return set, nil
	}
	if err != nil {
		return nil, fmt.Errorf("project: read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, set); err != nil {
		return nil, fmt.Errorf("project: parse %s: %w", path, err)
	}
	if set.SchemaVersion == 0 {
		set.SchemaVersion = SchemaVersion
	}
	if set.Workers == nil {
		set.Workers = []projworker.Declaration{}
	}
	return set, nil
}

func saveSet(projectID string, set *WorkerSet, archived bool) error {
	if set.SchemaVersion == 0 {
		set.SchemaVersion = SchemaVersion
	}
	if set.Workers == nil {
		set.Workers = []projworker.Declaration{}
	}
	path := WorkersPath(projectID)
	if archived {
		path = ArchivedPath(projectID)
	}
	if err := os.MkdirAll(ProjectDir(projectID), 0755); err != nil {
		return fmt.Errorf("project: mkdir: %w", err)
	}
	return writeJSONAtomic(path, set)
}

// LoadProject loads a project's metadata from project.json.
func LoadProject(id string) (*Project, error) {
	// One-time conversion of legacy projects. Idempotent and cheap once the
	// schema marker is present.
	if err := migrate.EnsureProject(ProjectDir(id)); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(ProjectPath(id))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("project: %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("project: read %s: %w", id, err)
	}
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("project: parse %s: %w", id, err)
	}
	return &p, nil
}

// SaveProject writes a project's metadata back to project.json.
func SaveProject(p *Project) error {
	if p.SchemaVersion == 0 {
		p.SchemaVersion = SchemaVersion
	}
	if p.ID == "" {
		return fmt.Errorf("project: cannot save project with empty id")
	}
	if err := os.MkdirAll(ProjectDir(p.ID), 0755); err != nil {
		return fmt.Errorf("project: mkdir: %w", err)
	}
	return writeJSONAtomic(ProjectPath(p.ID), p)
}

// writeJSONAtomic writes v as JSON through a temp file + rename, so a crash
// never leaves a half-written declaration file.
func writeJSONAtomic(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("project: marshal %s: %w", path, err)
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return fmt.Errorf("project: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("project: rename %s: %w", path, err)
	}
	return nil
}

// writeProjectJSON is kept for the linker: it seeds project.json into an
// external directory.
func writeProjectJSON(path string, p *Project) error {
	if p.SchemaVersion == 0 {
		p.SchemaVersion = SchemaVersion
	}
	return writeJSONAtomic(path, p)
}

// ListProjects returns every project's metadata, sorted by id.
func ListProjects() ([]Project, error) {
	entries, err := os.ReadDir(ProjectsRoot())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("project: read root: %w", err)
	}
	var out []Project
	for _, de := range entries {
		fi, err := os.Stat(filepath.Join(ProjectsRoot(), de.Name()))
		if err != nil || !fi.IsDir() {
			continue
		}
		p, err := LoadProject(de.Name())
		if err != nil {
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// LinkProject registers an external directory as a niq project by symlinking it
// into <niq root>/projects/<id>.
func LinkProject(dir string) (*Project, error) {
	if dir == "" {
		return nil, fmt.Errorf("project: directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("project: resolve %q: %w", dir, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("project: %q: %w", dir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("project: %q is not a directory", dir)
	}

	id := sanitizeID(filepath.Base(abs))
	if raw, err := os.ReadFile(filepath.Join(abs, "project.json")); err == nil {
		var p Project
		if json.Unmarshal(raw, &p) == nil && p.ID != "" {
			id = sanitizeID(p.ID)
		}
	}
	link := ProjectDir(id)
	if _, err := os.Lstat(link); err == nil {
		return nil, fmt.Errorf("project: %q already exists at %s", id, link)
	}
	if err := os.MkdirAll(ProjectsRoot(), 0755); err != nil {
		return nil, fmt.Errorf("project: mkdir: %w", err)
	}
	if err := os.Symlink(filepath.Clean(abs), link); err != nil {
		return nil, fmt.Errorf("project: link %s -> %s: %w", link, abs, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(link)
		}
	}()
	if err := ensureProjectJSON(abs, id); err != nil {
		return nil, err
	}
	ok = true
	return LoadProject(id)
}

// ensureProjectJSON makes sure a linked directory carries project.json metadata,
// creating it when absent or backfilling the id when present. Worker files are
// left alone: they are the project's source of truth.
func ensureProjectJSON(dir, id string) error {
	// A linked directory may be a legacy niq project: convert it BEFORE reading
	// project.json into the metadata struct, or the legacy workers array would
	// be dropped on the rewrite.
	if err := migrate.EnsureProject(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, "project.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeProjectJSON(path, &Project{SchemaVersion: SchemaVersion, ID: id, CreatedAt: time.Now().Format(time.RFC3339)})
	}
	if err != nil {
		return fmt.Errorf("project: read %s: %w", path, err)
	}
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("project: parse %s: %w", path, err)
	}
	p.ID = id
	if p.SchemaVersion == 0 {
		p.SchemaVersion = SchemaVersion
	}
	return writeProjectJSON(path, &p)
}

func sanitizeID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
