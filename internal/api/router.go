// Package api implements the FlowGate REST API.
package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/dashboard"
	"github.com/vk9551/flowgate-io/internal/store"
)

// Scheduler is the subset of scheduler.Scheduler used by the API.
// Defined here so the api package doesn't import the scheduler package
// (interfaces belong in the consuming package per project convention).
type Scheduler interface {
	Schedule(e *store.ScheduledEvent) error
}

// Server holds all runtime dependencies for the API.
type Server struct {
	cfgPath   string
	cfg       *config.Config
	cfgMu     sync.RWMutex // guards cfg for hot-reload
	store     store.Store
	sched     Scheduler // may be nil if scheduler not wired
	startTime time.Time
}

// NewServer constructs a Server. cfg must already be loaded and validated.
func NewServer(cfgPath string, cfg *config.Config, st store.Store) *Server {
	return &Server{
		cfgPath:   cfgPath,
		cfg:       cfg,
		store:     st,
		startTime: time.Now(),
	}
}

// SetScheduler wires the scheduler so DELAY decisions are enqueued.
func (s *Server) SetScheduler(sched Scheduler) {
	s.sched = sched
}

// Handler returns an http.Handler with the full middleware chain applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("POST /v1/events", s.handleEventsPost)
	mux.HandleFunc("GET /v1/subjects/{id}", s.handleSubjectGet)
	mux.HandleFunc("DELETE /v1/subjects/{id}", s.handleSubjectDelete)
	mux.HandleFunc("GET /v1/policies", s.handlePoliciesGet)
	mux.HandleFunc("POST /v1/policies/reload", s.handlePoliciesReload)
	mux.HandleFunc("GET /v1/stats", s.handleStats)
	mux.HandleFunc("GET /v1/events/recent", s.handleEventsRecent)

	// Dashboard — served from embedded dist; public (no auth required).
	mux.Handle("/dashboard/", http.StripPrefix("/dashboard", dashboard.Handler()))
	mux.Handle("/dashboard", http.RedirectHandler("/dashboard/", http.StatusMovedPermanently))

	return s.authMiddleware(mux)
}

// getConfig returns the current config under a read lock (used internally).
func (s *Server) getConfig() *config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// GetConfig is the exported version of getConfig, used by external packages
// (e.g. dispatcher) that need the live config.
func (s *Server) GetConfig() *config.Config {
	return s.getConfig()
}

// setConfig replaces the active config under a write lock.
func (s *Server) setConfig(cfg *config.Config) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg = cfg
}
