// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	_ "embed"
)

//go:embed web/dashboard.html
var dashboardHTML []byte

type Server struct {
	scheduler  *Scheduler
	logWatcher *LogWatcher
	config     *Config
	logger     *slog.Logger
	tr         *Translations
}

func NewServer(scheduler *Scheduler, logWatcher *LogWatcher, config *Config, logger *slog.Logger, tr *Translations) *Server {
	return &Server{
		scheduler:  scheduler,
		logWatcher: logWatcher,
		config:     config,
		logger:     logger,
		tr:         tr,
	}
}

type statusResponse struct {
	Overall    string                       `json:"overall"`
	Components map[string][]checkResultJSON `json:"components"`
	History    map[string][]HistoryDay      `json:"history"`
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

// securityHeaders adds common security headers to responses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "0")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("GET /api/i18n", s.handleI18n)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return securityHeaders(mux)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(dashboardHTML)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := s.scheduler.GetStatus()

	resp := statusResponse{
		Overall:    s.scheduler.OverallStatus().String(),
		Components: make(map[string][]checkResultJSON),
		History:    s.scheduler.GetHistory(),
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
	w.Header().Set("Cache-Control", "no-cache, no-store")
	json.NewEncoder(w).Encode(resp)
}

// handleMetrics returns up to 2016 downsampled metric points (≈7 days at 5-min resolution).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	points := s.scheduler.GetMetrics(2016)

	// Determine num_cpus from the latest linux-load result for load% normalisation.
	numCPUs := 1
	status := s.scheduler.GetStatus()
	for _, results := range status {
		for _, res := range results {
			if res.Component == "linux-load" && res.Details != nil {
				if v := res.Details["num_cpus"]; v != "" {
					var n int
					if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
						numCPUs = n
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	json.NewEncoder(w).Encode(map[string]any{
		"points":   points,
		"num_cpus": numCPUs,
	})
}

// handleI18n returns translations for the dashboard frontend.
func (s *Server) handleI18n(w http.ResponseWriter, r *http.Request) {
	i18n := map[string]any{
		"lang":       s.tr.Lang(),
		"dashboard":  s.tr.Section("dashboard"),
		"components": s.tr.Section("components"),
		"status":     s.tr.Section("status"),
		"logs":       s.tr.Section("logs"),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	json.NewEncoder(w).Encode(i18n)
}

// handleLogs returns recent log warnings with mitigation advice.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store")

	if s.logWatcher == nil {
		json.NewEncoder(w).Encode(map[string]any{"entries": []any{}})
		return
	}

	entries := s.logWatcher.GetEntries()
	// Return in reverse chronological order (newest first)
	reversed := make([]LogEntry, len(entries))
	for i, e := range entries {
		reversed[len(entries)-1-i] = e
	}
	json.NewEncoder(w).Encode(map[string]any{"entries": reversed})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	passing := s.scheduler.CriticalChecksPassing(s.config.Healthz.CriticalChecks)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	if passing {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"` + s.tr.T("status.healthy") + `"}`))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"` + s.tr.T("status.unhealthy") + `"}`))
	}
}
