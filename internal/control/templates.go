package control

import (
	"encoding/json"
	"io"
	stdhttp "net/http"
	"os"

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
// another template (copy_from, a raw file clone) or a full template body
// (template, e.g. a draft the webui exported from a project and the user
// edited) — exactly one of the two.
func (c *Control) handleCreateTemplate(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var body struct {
		ID       string          `json:"id"`
		CopyFrom string          `json:"copy_from"`
		Template json.RawMessage `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		stdhttp.Error(w, "id and one source (copy_from or template) are required", 400)
		return
	}
	if (body.CopyFrom == "") == (len(body.Template) == 0) {
		stdhttp.Error(w, "exactly one source is required: copy_from or template", 400)
		return
	}

	var src []byte
	if len(body.Template) > 0 {
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
		src = append(raw, '\n')
	} else {
		var err error
		src, err = project.ReadTemplateRaw(project.TemplatesDir(), body.CopyFrom)
		if err != nil {
			stdhttp.Error(w, "unknown template: "+body.CopyFrom, 400)
			return
		}
	}

	dest := project.TemplatePath(project.TemplatesDir(), body.ID)
	if _, err := os.Stat(dest); err == nil {
		stdhttp.Error(w, "template already exists", 409)
		return
	}
	if err := writeTemplate(dest, src); err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
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
// webui shows it as an editable draft and saves it through POST.
func (c *Control) handleTemplatePreview(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	tmpl, err := project.ExportProjectTemplate(id)
	if err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmpl)
}

// writeTemplate persists a template file (dir created lazily).
func writeTemplate(dest string, src []byte) error {
	if err := os.MkdirAll(project.TemplatesDir(), 0755); err != nil {
		return err
	}
	return os.WriteFile(dest, src, 0644)
}

// handleDeleteTemplate removes an on-disk template file.
func (c *Control) handleDeleteTemplate(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	name := r.PathValue("name")
	if err := os.Remove(project.TemplatePath(project.TemplatesDir(), name)); err != nil {
		stdhttp.Error(w, "template not found", 404)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
