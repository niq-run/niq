// Project → template export: build a Template from a project's active worker
// manifests, so a project can serve as a clone source just like another
// template. Manifests carry their definition and placement; runtime state and
// bus identity (grants, credential) stay behind — a template is a starting
// point, not a copy of who-may-say-what.
package project

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/niq-run/niq/app/standalone/store"
	projtemplate "github.com/niq-run/niq/app/standalone/template"
	projworker "github.com/niq-run/niq/app/standalone/worker"
)

// ExportProjectTemplate builds a template from a project's active workers.
// Archived workers live in a separate file and are not exported: a template is
// a starting point, not an archive. An empty result is an error rather than a
// hollow template file. With withProgram kept for API compatibility, programs
// are part of params now, so it no longer changes the payload.
func ExportProjectTemplate(projectID string, withProgram bool) (*TemplateConfig, error) {
	set, err := store.LoadWorkers(store.ProjectDir(projectID))
	if err != nil {
		return nil, err
	}
	workers := make([]projworker.Manifest, 0, len(set.Workers))
	workers = append(workers, set.Workers...)
	if len(workers) == 0 {
		return nil, fmt.Errorf("project: %q has no exportable workers", projectID)
	}
	return projtemplate.Validate(&TemplateConfig{SchemaVersion: SchemaVersion, Workers: workers})
}

// ProjectProgramsDir returns a project's programs resources directory.
func ProjectProgramsDir(id string) string {
	return store.ProgramsDir(store.ProjectDir(id))
}

// ExportProjectPrograms copies a project's programs resources into a template
// directory's programs/ subdirectory. A project without a programs dir is a
// no-op (false, nil).
func ExportProjectPrograms(projectID, tmplDir string) (bool, error) {
	src := ProjectProgramsDir(projectID)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := projtemplate.CopyDir(src, filepath.Join(tmplDir, "programs")); err != nil {
		return false, err
	}
	return true, nil
}
