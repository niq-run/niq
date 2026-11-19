// Package migrate converts legacy project and template files to the schema-2
// layout:
//
//	project.json            metadata only
//	workers.json            active declarations
//	workers.archived.json   archived declarations
//	root_secret             token-signing secret (project root, not id/)
//
// It is the ONLY code that reads the legacy shape. The runtime reads schema 2
// exclusively, so compatibility branches do not leak into the worker model or
// project storage.
package migrate

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/niq-run/niq/app/standalone/project/store/identity"
	projworker "github.com/niq-run/niq/app/standalone/worker"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

const schemaVersion = 2

// EnsureProject converts dir's project.json/identities.json to the schema-2
// layout if needed. It is idempotent and leaves project.json.bak /
// identities.json.bak behind for inspection.
func EnsureProject(dir string) error {
	projectPath := filepath.Join(dir, "project.json")
	raw, err := os.ReadFile(projectPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("migrate: read project.json: %w", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("migrate: parse project.json: %w", err)
	}

	workersPath := filepath.Join(dir, "workers.json")
	if version, ok := number(meta["schema_version"]); ok && int(version) >= schemaVersion {
		if _, err := os.Stat(workersPath); err == nil {
			// Already schema 2. Schema 2 is not released yet, so its worker-type
			// vocabulary is still being settled: bring a project written against an
			// older spelling up to date in place. This is a NAME normalization, not
			// a format change, so the schema version stays 2.
			if err := normalizeNames(dir); err != nil {
				return err
			}
			// Identity data used to live inline in workers.json. Move it to the
			// bus identity store, which is now its single owner.
			return migrateInlineIdentities(dir)
		}
		// Metadata is already schema 2 but the active file is missing: create
		// an empty one rather than re-running the legacy conversion.
		return writeJSON(workersPath, map[string]any{"schema_version": schemaVersion, "workers": []any{}})
	}

	// Legacy project: split out metadata, worker declarations and archive.
	legacyWorkers, _ := meta["workers"].([]any)
	archivedMap, _ := meta["archived"].(map[string]any)
	identities, _ := readIdentities(filepath.Join(dir, "id", "identities.json"))

	active := make([]*projworker.Manifest, 0, len(legacyWorkers))
	archived := make([]*projworker.Manifest, 0)
	var identitiesOut []corebus.Identity
	var orphans []string
	known := map[string]bool{}

	for _, rawWorker := range legacyWorkers {
		wm, ok := rawWorker.(map[string]any)
		if !ok {
			continue
		}
		var legacy *legacyIdentity
		if ident, ok := identities[stringValue(wm["id"])]; ok {
			legacy = &ident
		}
		d, idt, err := convertWorker(wm, legacy)
		if err != nil {
			return err
		}
		known[d.ID] = true
		if idt != nil {
			identitiesOut = append(identitiesOut, *idt)
		}
		isArchived := boolValue(wm["archived"]) || boolValue(archivedMap[d.ID])
		if isArchived {
			archived = append(archived, &d)
		} else {
			active = append(active, &d)
		}
	}
	for id := range identities {
		if !known[id] {
			orphans = append(orphans, id)
		}
	}
	sort.Strings(orphans)

	// Backups before any write.
	if err := backup(projectPath); err != nil {
		return err
	}
	if src := filepath.Join(dir, "id", "identities.json"); fileExists(src) {
		_ = copyTo(src, filepath.Join(dir, "identities.json.bak"))
	}

	// New project metadata: only project-level fields survive.
	newMeta := map[string]any{"schema_version": schemaVersion}
	for _, k := range []string{"id", "created_at", "ports", "upload_dir"} {
		if v, ok := meta[k]; ok {
			newMeta[k] = v
		}
	}
	if err := writeJSON(projectPath, newMeta); err != nil {
		return err
	}
	if err := writeJSON(workersPath, map[string]any{"schema_version": schemaVersion, "workers": active}); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "workers.archived.json"), map[string]any{"schema_version": schemaVersion, "workers": archived}); err != nil {
		return err
	}
	if err := identity.Merge(filepath.Join(dir, "identities.json"), identitiesOut); err != nil {
		return fmt.Errorf("migrate: write identities: %w", err)
	}

	// root_secret moves from id/ to the project root. The bytes are unchanged,
	// so existing remote tokens keep verifying.
	oldSecret := filepath.Join(dir, "id", "root_secret")
	newSecret := filepath.Join(dir, "root_secret")
	if fileExists(oldSecret) {
		if fileExists(newSecret) {
			// The root copy already exists; the old one is redundant.
			_ = os.Remove(oldSecret)
		} else if err := os.Rename(oldSecret, newSecret); err != nil {
			return fmt.Errorf("migrate: move root_secret: %w", err)
		}
	}
	_ = os.Chmod(newSecret, 0600)

	// Remove the old registry and (if empty) the id directory.
	_ = os.Remove(filepath.Join(dir, "id", "identities.json"))
	if entries, err := os.ReadDir(filepath.Join(dir, "id")); err == nil && len(entries) == 0 {
		_ = os.Remove(filepath.Join(dir, "id"))
	}

	if len(orphans) > 0 {
		log.Printf("[migrate] %s: discarded orphan identities: %s", dir, strings.Join(orphans, ", "))
	}
	return writeJSON(filepath.Join(dir, "migration-report.json"), map[string]any{
		"schema_version":    schemaVersion,
		"active_workers":    len(active),
		"archived_workers":  len(archived),
		"orphan_identities": orphans,
	})
}

// EnsureTemplates converts every legacy template.json under dir. Each template
// keeps its workers in the new declaration shape.
func EnsureTemplates(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("migrate: read templates: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "template.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var tmpl map[string]any
		if err := json.Unmarshal(raw, &tmpl); err != nil {
			log.Printf("[migrate] template %s: parse: %v", path, err)
			continue
		}
		if version, ok := number(tmpl["schema_version"]); ok && int(version) >= schemaVersion {
			continue
		}
		legacyWorkers, _ := tmpl["workers"].([]any)
		workers := make([]*projworker.Manifest, 0, len(legacyWorkers))
		for _, rawWorker := range legacyWorkers {
			wm, ok := rawWorker.(map[string]any)
			if !ok {
				continue
			}
			d, _, err := convertWorker(wm, nil)
			if err != nil {
				log.Printf("[migrate] template %s: %v", path, err)
				continue
			}
			workers = append(workers, &d)
		}
		if err := backup(path); err != nil {
			return err
		}
		newTmpl := map[string]any{"schema_version": schemaVersion, "workers": workers}
		if v, ok := tmpl["upload_dir"]; ok {
			newTmpl["upload_dir"] = v
		}
		if err := writeJSON(path, newTmpl); err != nil {
			return err
		}
	}
	return nil
}

// convertWorker turns one legacy worker map into a schema-2 manifest, plus the
// bus identity the worker carried inline (nil when it carried none: its
// identity is then seeded from the type default at first connect).
func convertWorker(wm map[string]any, legacy *legacyIdentity) (projworker.Manifest, *corebus.Identity, error) {
	d := projworker.Manifest{}
	d.ID = stringValue(wm["id"])
	d.Type = stringValue(wm["type"])
	if d.ID == "" || d.Type == "" {
		return d, nil, fmt.Errorf("worker missing id/type: %v", wm)
	}
	d.Label = stringValue(wm["label"])
	d.Tags = stringSlice(wm["tags"])
	d.Description = stringValue(wm["description"])
	d.Placement = placementFromMap(wm)
	idt := identityFromLegacy(d.ID, d.Type, d.Placement, wm, legacy)

	d.Command = stringSlice(wm["command"])
	d.Cwd = stringValue(wm["cwd"])

	// Everything else becomes params: typed construction fields, env, arbitrary
	// keys. A nested params map is merged, and a duplicate key is an error (no
	// silent overrides).
	params := map[string]any{}
	fixed := map[string]bool{
		"id": true, "type": true, "label": true, "tags": true, "description": true,
		"placement": true, "managed": true, "remote": true, "archived": true,
		"publish": true, "subscriptions": true, "credential": true,
		"token_ttl_seconds": true, "command": true, "cwd": true, "env": true, "params": true,
	}
	if nested, ok := wm["params"].(map[string]any); ok {
		for k, v := range nested {
			if fixed[k] {
				log.Printf("[migrate] worker %s: dropping identity/metadata key %q from params", d.ID, k)
				continue
			}
			params[k] = v
		}
	}
	if env, ok := wm["env"].(map[string]any); ok {
		for k, v := range env {
			if existing, dup := params[k]; dup && !sameJSON(existing, v) {
				return d, nil, fmt.Errorf("worker %s: env key %q conflicts with params", d.ID, k)
			}
			params[k] = v
		}
	}
	for k, v := range wm {
		if fixed[k] {
			continue
		}
		if existing, dup := params[k]; dup && !sameJSON(existing, v) {
			return d, nil, fmt.Errorf("worker %s: top-level %q conflicts with params", d.ID, k)
		}
		params[k] = v
	}
	if len(params) > 0 {
		d.Params = params
	}
	if d.Placement == projworker.PlacementUnmanagedProcess && len(d.Params) > 0 {
		log.Printf("[migrate] worker %s: unmanaged-process cannot carry params; dropping %d", d.ID, len(d.Params))
		d.Params = nil
	}
	return d, idt, nil
}

type legacyIdentity struct {
	WorkerID       string
	Type           string
	Credential     string
	Remote         bool
	PublishAllow   []event.PublishPattern
	SubscribeAllow []event.EventPattern
}

func readIdentities(path string) (map[string]legacyIdentity, error) {
	out := map[string]legacyIdentity{}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("read identities: %w", err)
	}
	return out, nil
}

// placementFromMap resolves a legacy worker's placement: an explicit field
// wins, otherwise the managed/remote pair.
func placementFromMap(wm map[string]any) projworker.Placement {
	switch p := stringValue(wm["placement"]); p {
	case "in-process", "managed-process", "unmanaged-process":
		return projworker.Placement(p)
	}
	managed := true
	if raw, ok := wm["managed"]; ok && raw != nil {
		managed = boolValue(raw)
	}
	switch {
	case managed:
		return projworker.PlacementInProcess
	case boolValue(wm["remote"]):
		return projworker.PlacementUnmanagedProcess
	default:
		return projworker.PlacementManagedProcess
	}
}

// identityFromLegacy lifts the inline identity data of a legacy worker into the
// bus identity record. It returns nil when the worker carried no explicit grant
// and no credential — such an identity is seeded from the type default when the
// worker first connects, which is what happened before this split too.
func identityFromLegacy(id, typ string, placement projworker.Placement, wm map[string]any, legacy *legacyIdentity) *corebus.Identity {
	idt := corebus.Identity{
		WorkerID: id,
		Type:     typ,
		Remote:   placement != projworker.PlacementInProcess,
	}
	seeded := false
	if raw, ok := wm["publish"]; ok {
		_ = jsonRoundTrip(raw, &idt.PublishAllow)
		seeded = true
	} else if legacy != nil && len(legacy.PublishAllow) > 0 {
		idt.PublishAllow = legacy.PublishAllow
		seeded = true
	}
	if raw, ok := wm["subscriptions"]; ok {
		_ = jsonRoundTrip(raw, &idt.SubscribeAllow)
		seeded = true
	} else if legacy != nil && len(legacy.SubscribeAllow) > 0 {
		idt.SubscribeAllow = legacy.SubscribeAllow
		seeded = true
	}
	// Only a self-started worker keeps a durable credential; a host-launched
	// child's credential lives and dies with the child.
	if placement == projworker.PlacementUnmanagedProcess {
		idt.Credential = stringValue(wm["credential"])
		if idt.Credential == "" && legacy != nil {
			idt.Credential = legacy.Credential
		}
	}
	if !seeded && idt.Credential == "" {
		return nil
	}
	return &idt
}

// migrateInlineIdentities moves inline publish/subscriptions/credential fields
// out of a schema-2 project's worker files and into identities.json. It is
// idempotent: the identity file's presence marks the migration done, so the
// per-call cost after the first run is one stat.
func migrateInlineIdentities(dir string) error {
	identityPath := filepath.Join(dir, "identities.json")
	if fileExists(identityPath) {
		return nil
	}
	var ids []corebus.Identity
	for _, name := range []string{"workers.json", "workers.archived.json"} {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		var file struct {
			SchemaVersion int              `json:"schema_version"`
			Workers       []map[string]any `json:"workers"`
		}
		if err := json.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("migrate: parse %s: %w", name, err)
		}
		changed := false
		for _, wm := range file.Workers {
			id, typ := stringValue(wm["id"]), stringValue(wm["type"])
			if id == "" || typ == "" {
				continue
			}
			if idt := identityFromLegacy(id, typ, placementFromMap(wm), wm, nil); idt != nil {
				ids = append(ids, *idt)
			}
			for _, k := range []string{"publish", "subscriptions", "credential", "token_ttl_seconds"} {
				if _, ok := wm[k]; ok {
					delete(wm, k)
					changed = true
				}
			}
		}
		if changed {
			if err := writeJSON(path, file); err != nil {
				return err
			}
		}
	}
	if err := identity.Merge(identityPath, ids); err != nil {
		return fmt.Errorf("migrate: write identities: %w", err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("migrate: marshal %s: %w", path, err)
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return fmt.Errorf("migrate: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("migrate: rename %s: %w", path, err)
	}
	return nil
}

func copyTo(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, raw, 0644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func backup(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	bak := path + ".bak"
	if _, err := os.Stat(bak); err == nil {
		return nil
	}
	return os.WriteFile(bak, raw, 0644)
}

func jsonRoundTrip(v any, dst any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func sameJSON(a, b any) bool {
	ra, err1 := json.Marshal(a)
	rb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ra) == string(rb)
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func stringSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	}
	return nil
}

// ── Name normalization (schema 2 vocabulary) ──

// renamedTypes maps a retired worker type to the one that replaced it.
//
//	niw   → niq        the worker whose backend is another niq instance
//	hiw   → interface  retired: the WebUI is an app-hosted interface worker now,
//	                   and its far-side identity is a plain niq worker
//	remote-niw → niq   the pseudo-type for "the far side dials in" is gone: that
//	                   is a niq declaration with unmanaged-process placement
var renamedTypes = map[string]string{
	"niw":        "niq",
	"hiw":        "interface",
	"remote-niw": "niq",
}

// renamedWorkerIDs maps a retired worker id to its replacement. The WebUI's
// worker is `webui` now, and its old id baked in the retired `hiw` type.
var renamedWorkerIDs = map[string]string{
	"webui-hiw": "webui",
}

// normalizeNames rewrites the retired vocabulary in a project's declaration files.
// It is idempotent and touches nothing unless it finds something to rename: an
// already-current project is a single read per file.
//
// What it does NOT do: rewrite free text (a program body that calls niw.status, an
// instruction naming webui-hiw). Those are user content; they are reported instead
// so a human can fix them, because silently rewriting someone's prose is worse
// than telling them.
func normalizeNames(dir string) error {
	changed := false
	for _, name := range []string{"workers.json", "workers.archived.json"} {
		path := filepath.Join(dir, name)
		set, err := readWorkerSet(path)
		if err != nil || set == nil {
			continue
		}
		if rewriteWorkerSet(set) {
			changed = true
			if err := writeJSON(path, set); err != nil {
				return err
			}
		}
	}
	if changed {
		log.Printf("[migrate] %s: worker names normalized (niw→niq, hiw→interface, webui-hiw→webui)", dir)
	}
	return nil
}

// rewriteWorkerSet applies the renames to one declaration set, reporting whether
// anything changed. ACL entries naming a renamed worker are rewritten too: a
// grant to `webui-hiw` would otherwise address a worker that no longer exists.
func rewriteWorkerSet(set map[string]any) bool {
	workers, _ := set["workers"].([]any)
	changed := false
	kept := make([]any, 0, len(workers))
	for _, raw := range workers {
		d, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		if id, ok := d["id"].(string); ok {
			if next, renamed := renamedWorkerIDs[id]; renamed {
				d["id"] = next
				changed = true
			}
		}
		if typ, ok := d["type"].(string); ok {
			if next, renamed := renamedTypes[typ]; renamed {
				d["type"] = next
				changed = true
			}
		}
		for _, key := range []string{"publish", "subscriptions"} {
			if rewriteACL(d[key]) {
				changed = true
			}
		}
		if params, ok := d["params"].(map[string]any); ok {
			// The approval target is a worker id, not a type.
			if approver, ok := params["approver"].(string); ok {
				if next, renamed := renamedWorkerIDs[approver]; renamed {
					params["approver"] = next
					changed = true
				}
			}
			// A niq link's far-side identity is no longer a pseudo-type.
			if typ, ok := params["type"].(string); ok {
				if next, renamed := renamedTypes[typ]; renamed {
					params["type"] = next
					changed = true
				}
			}
		}
		// The WebUI's own declaration is a leftover: its worker is app-hosted now
		// (the assembly joins it and declares its grants in code), so keeping the
		// declaration would only make the roster show a phantom "stopped" worker
		// whose start can never succeed — the type has no builder.
		if id, _ := d["id"].(string); id == "webui" {
			changed = true
			continue
		}
		kept = append(kept, d)
	}
	if changed {
		set["workers"] = kept
	}
	return changed
}

// rewriteACL renames worker ids referenced by publish targets / subscription
// sources.
func rewriteACL(v any) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	changed := false
	for _, raw := range list {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"target", "source"} {
			if id, ok := entry[key].(string); ok {
				if next, renamed := renamedWorkerIDs[id]; renamed {
					entry[key] = next
					changed = true
				}
			}
		}
	}
	return changed
}

// readWorkerSet loads one declaration file, returning nil when it does not exist.
func readWorkerSet(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("migrate: read %s: %w", path, err)
	}
	var set map[string]any
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("migrate: parse %s: %w", path, err)
	}
	return set, nil
}
