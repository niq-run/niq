package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	stdhttp "net/http"
	"strconv"
	"time"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/pkg/service/eventbus"
)

// Server serves the bus event API over HTTP.
//
// Endpoints:
//
//	GET /api/stream?worker=&trace=&type=&limit=  — SSE real-time + history
//	GET /api/events/before/{id}?worker=&trace=&limit=  — historical pagination
//	GET /api/workers  — list online workers
type Server struct {
	log    *EventLog
	engine *eventbus.Engine
	addr   string
	mux    *stdhttp.ServeMux
}

// NewServer creates an HTTP server for the bus event API.
func NewServer(log *EventLog, engine *eventbus.Engine, addr string) *Server {
	s := &Server{
		log:    log,
		engine: engine,
		addr:   addr,
		mux:    stdhttp.NewServeMux(),
	}

	s.mux.HandleFunc("GET /api/stream", s.handleStream)
	s.mux.HandleFunc("GET /api/events/before/{id}", s.handleLoadBefore)
	s.mux.HandleFunc("GET /api/workers", s.handleWorkers)

	return s
}

// Start starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	server := &stdhttp.Server{
		Addr:    s.addr,
		Handler: cors(s.mux),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	log.Printf("[eventbus api] listening on %s", s.addr)
	if err := server.ListenAndServe(); err != stdhttp.ErrServerClosed {
		return err
	}
	return nil
}

// Handler returns the HTTP handler for mounting on an existing server.
func (s *Server) Handler() stdhttp.Handler {
	return cors(s.mux)
}

// ── SSE stream ──

func (s *Server) handleStream(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	flusher, ok := w.(stdhttp.Flusher)
	if !ok {
		stdhttp.Error(w, "streaming not supported", 500)
		return
	}

	filter := Filter{
		WorkerIDs: r.URL.Query()["worker"],
		TraceID:   r.URL.Query().Get("trace"),
		Type:      event.EventType(r.URL.Query().Get("type")),
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, err := s.log.Follow(r.Context(), filter, limit)
	if err != nil {
		log.Printf("[eventbus api] follow error: %v", err)
		stdhttp.Error(w, err.Error(), 500)
		return
	}

	for evt := range ch {
		data, _ := json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
}

// ── Historical pagination ──

func (s *Server) handleLoadBefore(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	anchor := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	filter := Filter{
		WorkerIDs: r.URL.Query()["worker"],
		TraceID:   r.URL.Query().Get("trace"),
		Type:      event.EventType(r.URL.Query().Get("type")),
	}

	events, err := s.log.LoadBefore(r.Context(), filter, anchor, limit)
	if err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

// ── Workers list ──

func (s *Server) handleWorkers(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	workers := s.engine.OnlineWorkers()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(workers)
}

// ── CORS ──

func cors(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(stdhttp.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
