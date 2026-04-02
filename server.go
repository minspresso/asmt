package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	_ "embed"
)

//go:embed web/dashboard.html
var dashboardHTML []byte

type Server struct {
	scheduler *Scheduler
	config    *Config
	logger    *slog.Logger
}

func NewServer(scheduler *Scheduler, config *Config, logger *slog.Logger) *Server {
	return &Server{
		scheduler: scheduler,
		config:    config,
		logger:    logger,
	}
}

type statusResponse struct {
	Overall    string                       `json:"overall"`
	Components map[string][]checkResultJSON `json:"components"`
	CheckedAt  time.Time                    `json:"checked_at"`
}

type checkResultJSON struct {
	Component string            `json:"component"`
	Status    string            `json:"status"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
	CheckedAt string            `json:"checked_at"`
	Duration  string            `json:"duration"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return mux
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(dashboardHTML)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := s.scheduler.GetStatus()

	resp := statusResponse{
		Overall:    s.scheduler.OverallStatus().String(),
		Components: make(map[string][]checkResultJSON),
		CheckedAt:  time.Now(),
	}

	for name, results := range status {
		var items []checkResultJSON
		for _, r := range results {
			items = append(items, checkResultJSON{
				Component: r.Component,
				Status:    r.Status.String(),
				Message:   r.Message,
				Details:   r.Details,
				CheckedAt: r.CheckedAt.Format(time.RFC3339),
				Duration:  r.Duration.String(),
			})
		}
		resp.Components[name] = items
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	passing := s.scheduler.CriticalChecksPassing(s.config.Healthz.CriticalChecks)

	w.Header().Set("Content-Type", "application/json")
	if passing {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"unhealthy"}`))
	}
}
