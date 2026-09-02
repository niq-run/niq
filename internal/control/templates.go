package control

import (
	"encoding/json"
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

// handleCreateTemplate clones an existing template into a new on-disk one.
func (c *Control) handleCreateTemplate(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var body struct {
		ID       string `json:"id"`
		CopyFrom string `json:"copy_from"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" || body.CopyFrom == "" {
		stdhttp.Error(w, "id and copy_from are required", 400)
		return
	}
	src, err := project.ReadTemplateRaw(project.TemplatesDir(), body.CopyFrom)
	if err != nil {
		stdhttp.Error(w, "unknown template: "+body.CopyFrom, 400)
		return
	}
	dest := project.TemplatePath(project.TemplatesDir(), body.ID)
	if _, err := os.Stat(dest); err == nil {
		stdhttp.Error(w, "template already exists", 409)
		return
	}
	if err := os.MkdirAll(project.TemplatesDir(), 0755); err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	if err := os.WriteFile(dest, src, 0644); err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(stdhttp.StatusCreated)
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
