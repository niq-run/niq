package control

import (
	"encoding/json"
	"io"
	stdhttp "net/http"
	"os"
	"path/filepath"

	"github.com/niq-run/niq/internal/project"
)

// handleListTemplates returns the available project template names for the
// new-project dropdown.
func (c *Control) handleListTemplates(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	names, err := project.ListTemplates()
	if err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(names)
}

// handleTemplateDetail returns a template's JSON content (its workers etc.).
func (c *Control) handleTemplateDetail(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	name := r.PathValue("name")
	raw, err := project.ReadTemplateRaw(project.TemplatesDir(), name)
	if err != nil {
		stdhttp.Error(w, "template not found", 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// handleCreateTemplate creates a new on-disk template. The source is either
// another template (copy_from, a whole-directory clone — programs/ travel
// along) or a full template body (template, e.g. a draft the webui exported
// from a project and the user edited) — exactly one of the two. An export may
// additionally carry the source project's programs resources: from_project +
// include_program copy <project>/programs into the new template dir.
func (c *Control) handleCreateTemplate(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var body struct {
		ID             string          `json:"id"`
		CopyFrom       string          `json:"copy_from"`
		Template       json.RawMessage `json:"template"`
		FromProject    string          `json:"from_project"`
		IncludeProgram bool            `json:"include_program"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		stdhttp.Error(w, "id and one source (copy_from or template) are required", 400)
		return
	}
	if (body.CopyFrom == "") == (len(body.Template) == 0) {
		stdhttp.Error(w, "exactly one source is required: copy_from or template", 400)
		return
	}

	destDir := project.TemplateDir(project.TemplatesDir(), body.ID)
	if _, err := os.Stat(destDir); err == nil {
		stdhttp.Error(w, "template already exists", 409)
		return
	}

	if len(body.Template) == 0 {
		srcDir := project.TemplateDir(project.TemplatesDir(), body.CopyFrom)
		if _, err := os.Stat(project.TemplatePath(project.TemplatesDir(), body.CopyFrom)); err != nil {
			// Not on disk: clone from the embedded built-in, if there is one.
			raw, err := project.ReadTemplateRaw(project.TemplatesDir(), body.CopyFrom)
			if err != nil {
				stdhttp.Error(w, "unknown template: "+body.CopyFrom, 400)
				return
			}
			if err := writeTemplate(project.TemplatePath(project.TemplatesDir(), body.ID), raw); err != nil {
				stdhttp.Error(w, err.Error(), 500)
				return
			}
			w.WriteHeader(stdhttp.StatusCreated)
			return
		}
		if err := os.MkdirAll(project.TemplatesDir(), 0755); err != nil {
			stdhttp.Error(w, err.Error(), 500)
			return
		}
		if err := project.CopyDir(srcDir, destDir); err != nil {
			stdhttp.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(stdhttp.StatusCreated)
		return
	}

	tmpl, err := project.ValidateTemplate(body.Template)
	if err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	raw, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	if err := writeTemplate(project.TemplatePath(project.TemplatesDir(), body.ID), append(raw, '\n')); err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	if body.IncludeProgram && body.FromProject != "" {
		if _, err := project.ExportProjectPrograms(body.FromProject, destDir); err != nil {
			stdhttp.Error(w, "copy programs: "+err.Error(), 500)
			return
		}
	}
	w.WriteHeader(stdhttp.StatusCreated)
}

// handleUpdateTemplate overwrites an existing template with a full template
// body (the webui's drawer editor saves through it). The name is the key: it
// cannot be changed here.
func (c *Control) handleUpdateTemplate(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	name := r.PathValue("name")
	dest := project.TemplatePath(project.TemplatesDir(), name)
	if _, err := os.Stat(dest); err != nil {
		stdhttp.Error(w, "template not found", 404)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	tmpl, err := project.ValidateTemplate(body)
	if err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	raw, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	if err := writeTemplate(dest, append(raw, '\n')); err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

// handleTemplatePreview returns the template a project would export (its
// workers re-expressed as template workers) WITHOUT writing anything — the
// webui shows it as an editable draft and saves it through POST. With
// include_program=1, a reason worker's programs param is carried too (the
// programs/ resources are copied at save time).
func (c *Control) handleTemplatePreview(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	withProgram := r.URL.Query().Get("include_program") == "1"
	tmpl, err := project.ExportProjectTemplate(id, withProgram)
	if err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmpl)
}

// writeTemplate persists a template's template.json (dirs created lazily).
func writeTemplate(dest string, src []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return os.WriteFile(dest, src, 0644)
}

// handleDeleteTemplate removes an on-disk template directory.
func (c *Control) handleDeleteTemplate(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	name := r.PathValue("name")
	if err := os.RemoveAll(project.TemplateDir(project.TemplatesDir(), name)); err != nil {
		stdhttp.Error(w, "template not found", 404)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
