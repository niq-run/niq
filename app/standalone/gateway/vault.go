package gateway

import (
	"encoding/json"
	"errors"
	stdhttp "net/http"

	"github.com/niq-run/niq/app/standalone/vault"
)

// vaultEntry is one row in the management list. It carries NO value: a UI that
// can read a secret can leak it, so the list endpoint only reports that an entry
// exists, how big it is and when it changed. Reading the value is the runtime's
// job (Resolve), not the browser's.
type vaultEntry struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updated_at"`
}

type vaultPutRequest struct {
	Value string `json:"value"`
}

// handleListVault lists credentials shared by every project on this machine.
func (c *Gateway) handleListVault(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
	entries, err := c.vault.List()
	if err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	out := make([]vaultEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, vaultEntry{
			Name:      e.Name,
			Size:      e.Size,
			UpdatedAt: e.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"entries": out})
}

// handlePutVault stores (or overwrites) one entry. The name may contain slashes,
// which the router hands back whole; it is validated as a name, not as a path.
func (c *Gateway) handlePutVault(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	name := r.PathValue("name")
	if err := vault.ValidateName(name); err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	var req vaultPutRequest
	if err := json.NewDecoder(stdhttp.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		stdhttp.Error(w, "invalid body: expected {\"value\":\"...\"}", 400)
		return
	}
	if req.Value == "" {
		stdhttp.Error(w, "value must not be empty", 400)
		return
	}
	if err := c.vault.Put(name, req.Value); err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"name": name, "ok": true})
}

// handleDeleteVault removes one entry. This is an explicit management action: a
// worker being destroyed never deletes credentials, because an entry may be
// shared.
func (c *Gateway) handleDeleteVault(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	name := r.PathValue("name")
	if err := vault.ValidateName(name); err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	if err := c.vault.Delete(name); err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			stdhttp.Error(w, err.Error(), 404)
			return
		}
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
