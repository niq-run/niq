package project

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/niq-run/niq/pkg/services/pgbackend"
)

// ProgramView is a read-only summary of one Program under a project, for the
// WebUI program browser. It mirrors core/program.Meta plus the entry content's
// form type and a count of sub-contents.
type ProgramView struct {
	Name        string   `json:"name"`
	ContentType string   `json:"content_type,omitempty"`
	FormType    string   `json:"form_type,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Locked      bool     `json:"locked,omitempty"`
	Contents    int      `json:"contents,omitempty"`
}

// ListPrograms returns every Program under a project's programs/ directory as
// a read-only summary. A project with (as yet) no program space yields an
// empty list. It reuses the pgbackend index so the view matches exactly what
// the program worker mounts at runtime.
func ListPrograms(id string) ([]ProgramView, error) {
	dir := filepath.Join(ProjectDir(id), "programs")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, nil // no program space (or unreadable) → empty
	}
	progs, err := pgbackend.New(dir).List(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]ProgramView, 0, len(progs))
	for _, p := range progs {
		out = append(out, ProgramView{
			Name:        p.Name,
			ContentType: string(p.ContentType),
			FormType:    string(p.EntryContent.FormType),
			Description: p.Description,
			Tags:        p.Tags,
			Locked:      p.Locked,
			Contents:    len(p.Contents),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}