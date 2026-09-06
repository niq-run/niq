package control

// Provider config management: the control plane serves ~/.niq/common/providers/
// provider.json (the same common layer as templates) so the WebUI can view and
// edit it. The whole config is one resource — GET returns it, PUT replaces it.

import (
	"encoding/json"
	"io"
	stdhttp "net/http"

	"github.com/niq-run/niq/internal/project/provider"
)

// handleGetProviders returns the provider config verbatim (including api_key
// values — it is the user's own file on their own machine, and ${VAR}
// references only make sense if the raw text is preserved).
func (c *Control) handleGetProviders(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	cfg, err := provider.Load()
	if err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

// handleUpdateProviders replaces the provider config with the request body.
// Validation is structural: every entry needs a name, and names must be
// unique — the config's entries are referenced by name (the active field and
// runtime provider switches).
func (c *Control) handleUpdateProviders(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	var cfg provider.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		stdhttp.Error(w, "bad provider config: "+err.Error(), 400)
		return
	}
	seen := map[string]bool{}
	for _, p := range cfg.Providers {
		if p.Name == "" {
			stdhttp.Error(w, "provider entry missing name", 400)
			return
		}
		if seen[p.Name] {
			stdhttp.Error(w, "duplicate provider name: "+p.Name, 400)
			return
		}
		seen[p.Name] = true
	}
	if err := provider.Write(&cfg); err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}
