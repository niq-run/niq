// Project → template export: build a Template from a project's active worker
// declarations, so a project can serve as a clone source just like another
// template. Declarations carry their params and identity config; runtime state
// stays behind. Secrets (a remote worker's bus credential) never enter a
// template.
package project

import (
	"fmt"
	"os"
	"path/filepath"

	projtemplate "github.com/niq-run/niq/app/standalone/project/template"
	projworker "github.com/niq-run/niq/app/standalone/worker"
)

// ExportProjectTemplate builds a template from a project's active workers.
// Archived workers live in a separate file and are not exported: a template is
// a starting point, not an archive. An empty result is an error rather than a
// hollow template file. With withProgram kept for API compatibility, programs
// are part of params now, so it no longer changes the payload.
func ExportProjectTemplate(projectID string, withProgram bool) (*TemplateConfig, error) {
	set, err := LoadWorkers(projectID)
	if err != nil {
		return nil, err
	}
	workers := make([]projworker.Declaration, 0, len(set.Workers))
	for _, d := range set.Workers {
		d.Credential = "" // never export a bus credential
		workers = append(workers, d)
	}
	if len(workers) == 0 {
		return nil, fmt.Errorf("project: %q has no exportable workers", projectID)
	}
	return projtemplate.Validate(&TemplateConfig{SchemaVersion: SchemaVersion, Workers: workers})
}

// ProjectProgramsDir returns a project's programs resources directory.
func ProjectProgramsDir(id string) string {
	return filepath.Join(ProjectDir(id), "programs")
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
