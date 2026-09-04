// Package registryapi provides an HTTP API for managing the IdentityRegistry.
//
// It allows remote control plane tools to register, revoke, and list
// worker identities. All requests are authenticated via API key.
//
// API keys are stored in ~/.niq/id/api_keys.json. On first startup,
// a key is automatically generated and printed to the console.
package registryapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// ── API Key Store ──

// keyStore manages API keys persisted to a JSON file.
type keyStore struct {
	path string
	mu   sync.RWMutex
	keys map[string]bool
}

func loadKeyStore(path string) (*keyStore, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("registryapi: create key dir: %w", err)
	}

	ks := &keyStore{
		path: path,
		keys: make(map[string]bool),
	}

	data, err := os.ReadFile(path)
	if err == nil {
		var raw struct {
			Keys []string `json:"keys"`
		}
		if err := json.Unmarshal(data, &raw); err == nil {
			for _, k := range raw.Keys {
				ks.keys[k] = true
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("registryapi: read keys: %w", err)
	}

	// If no keys exist, generate one.
	if len(ks.keys) == 0 {
		key, err := generateKey()
		if err != nil {
			return nil, fmt.Errorf("registryapi: generate key: %w", err)
		}
		ks.keys[key] = true
		if err := ks.save(); err != nil {
			return nil, err
		}
		log.Printf("[registryapi] generated API key: %s", key)
		log.Printf("[registryapi] save this key — it will not be shown again")
	}

	return ks, nil
}

func (ks *keyStore) save() error {
	var raw struct {
		Keys []string `json:"keys"`
	}
	for k := range ks.keys {
		raw.Keys = append(raw.Keys, k)
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ks.path, data, 0644)
}

func (ks *keyStore) validate(key string) bool {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.keys[key]
}

func (ks *keyStore) add(key string) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.keys[key] = true
	return ks.save()
}

func (ks *keyStore) list() []string {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	var out []string
	for k := range ks.keys {
		out = append(out, k)
	}
	return out
}

func generateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(b), nil
}

// ── HTTP Server ──

// Server is the HTTP API server for the IdentityRegistry.
//
// Endpoints:
//
//	GET    /api/v1/identities       — list all identities
//	GET    /api/v1/identities/:id   — get a specific identity
//	POST   /api/v1/identities       — register a new identity
//	DELETE /api/v1/identities/:id   — revoke an identity
//
//	POST   /api/v1/keys             — generate a new API key (loopback only)
//	GET    /api/v1/keys              — list API keys (loopback only)
//
// All endpoints except /keys require an API key in the Authorization header.
type Server struct {
	registry corebus.IdentityRegistry
	keys     *keyStore
	addr     string
	mux      *stdhttp.ServeMux
}

// NewServer creates an HTTP API server for the IdentityRegistry.
// The API keys are stored in the given keyPath (e.g., ~/.niq/id/api_keys.json).
func NewServer(registry corebus.IdentityRegistry, keyPath, addr string) (*Server, error) {
	ks, err := loadKeyStore(keyPath)
	if err != nil {
		return nil, err
	}

	s := &Server{
		registry: registry,
		keys:     ks,
		addr:     addr,
		mux:      stdhttp.NewServeMux(),
	}

	s.mux.HandleFunc("/api/v1/identities", s.handleIdentities)
	s.mux.HandleFunc("/api/v1/identities/", s.handleIdentityByID)
	s.mux.HandleFunc("/api/v1/keys", s.handleKeys)

	return s, nil
}

// Start starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	server := &stdhttp.Server{
		Addr:    s.addr,
		Handler: s.mux,
	}

	go func() {
		<-ctx.Done()
		server.Close()
	}()

	log.Printf("[registryapi] listening on %s", s.addr)
	if err := server.ListenAndServe(); err != stdhttp.ErrServerClosed {
		return err
	}
	return nil
}

// ── Auth ──

func (s *Server) requireAuth(w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	key := r.Header.Get("Authorization")
	if key == "" {
		stdhttp.Error(w, "authorization required", 401)
		return false
	}
	// Strip "Bearer " prefix if present.
	if len(key) > 7 && key[:7] == "Bearer " {
		key = key[7:]
	}
	if !s.keys.validate(key) {
		stdhttp.Error(w, "invalid API key", 401)
		return false
	}
	return true
}

func isLoopback(r *stdhttp.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ── Handlers: Identities ──

func (s *Server) handleIdentities(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	switch r.Method {
	case "GET":
		s.handleListIdentities(w, r)
	case "POST":
		s.handleRegisterIdentity(w, r)
	default:
		stdhttp.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleListIdentities(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	// FileIdentityRegistry doesn't have a List method, so we can't list
	// all identities from the interface. For now, return an empty list.
	// TODO: add List() to IdentityRegistry interface.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"identities": []string{},
	})
}

type registerRequest struct {
	WorkerID       string                 `json:"worker_id"`
	Credential     string                 `json:"credential"`
	PublishAllow   []event.PublishPattern `json:"publish_allow"`
	SubscribeAllow []event.EventPattern   `json:"subscribe_allow"`
}

func (s *Server) handleRegisterIdentity(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !s.requireAuth(w, r) {
		return
	}

	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		stdhttp.Error(w, "invalid request", 400)
		return
	}
	if req.WorkerID == "" {
		stdhttp.Error(w, "worker_id is required", 400)
		return
	}

	id := corebus.Identity{
		WorkerID:       req.WorkerID,
		Credential:     req.Credential,
		PublishAllow:   req.PublishAllow,
		SubscribeAllow: req.SubscribeAllow,
	}
	// Omitted allow lists default to "*": external workers declare their own
	// event vocabularies (unknown to this repo), the registration is
	// credential-authenticated, and the control plane can narrow the grants
	// afterwards via allow editing. An explicit list in the request wins.
	if id.PublishAllow == nil {
		id.PublishAllow = []event.PublishPattern{event.NewPublishPattern("*")}
	}
	if id.SubscribeAllow == nil {
		id.SubscribeAllow = []event.EventPattern{{Type: "*"}}
	}

	if err := s.registry.Register(id); err != nil {
		stdhttp.Error(w, err.Error(), 409)
		return
	}

	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]string{
		"status":    "created",
		"worker_id": req.WorkerID,
	})
}

func (s *Server) handleIdentityByID(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !s.requireAuth(w, r) {
		return
	}

	// Extract worker ID from path: /api/v1/identities/{id}
	workerID := r.URL.Path[len("/api/v1/identities/"):]
	if workerID == "" {
		stdhttp.Error(w, "worker_id required", 400)
		return
	}

	switch r.Method {
	case "GET":
		id, ok := s.registry.Lookup(workerID)
		if !ok {
			stdhttp.Error(w, "not found", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(id)

	case "DELETE":
		if err := s.registry.Revoke(workerID); err != nil {
			stdhttp.Error(w, err.Error(), 404)
			return
		}
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]string{
			"status":    "revoked",
			"worker_id": workerID,
		})

	default:
		stdhttp.Error(w, "method not allowed", 405)
	}
}

// ── Handlers: Keys ──

func (s *Server) handleKeys(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	// Key management is loopback-only.
	if !isLoopback(r) {
		stdhttp.Error(w, "only loopback allowed", 403)
		return
	}

	switch r.Method {
	case "GET":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"keys": s.keys.list(),
		})

	case "POST":
		key, err := generateKey()
		if err != nil {
			stdhttp.Error(w, "key generation failed", 500)
			return
		}
		if err := s.keys.add(key); err != nil {
			stdhttp.Error(w, "save key failed", 500)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{
			"key": key,
		})

	default:
		stdhttp.Error(w, "method not allowed", 405)
	}
}
