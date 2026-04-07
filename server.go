// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	_ "embed"
)

//go:embed web/dashboard.html
var dashboardHTML []byte

type Server struct {
	scheduler  *Scheduler
	logWatcher *LogWatcher
	syncer     *Syncer
	config     *Config
	logger     *slog.Logger
	tr         *Translations
}

func NewServer(scheduler *Scheduler, logWatcher *LogWatcher, syncer *Syncer, config *Config, logger *slog.Logger, tr *Translations) *Server {
	return &Server{
		scheduler:  scheduler,
		logWatcher: logWatcher,
		syncer:     syncer,
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
//
// CSP rationale: the dashboard is a single-page app that uses inline
// <script> blocks and inline onclick handlers for simplicity (no build
// step, no separate JS file). That means 'unsafe-inline' must be
// allowed in script-src. We still lock down every other directive:
//
//   default-src 'self'      — block loads from any other origin
//   script-src 'self' 'unsafe-inline' — our own inline scripts, no CDNs
//   style-src 'self' 'unsafe-inline'  — our own inline styles, no external CSS
//   img-src 'self' data:    — inline data URIs only (no tracking pixels)
//   connect-src 'self'      — fetch/XHR only to our own origin
//   object-src 'none'       — block <object>, <embed>, Flash
//   base-uri 'self'         — prevent <base href> hijacks
//   form-action 'none'      — no form submissions
//   frame-ancestors 'none'  — redundant with X-Frame-Options
//
// This policy would break if the dashboard ever tried to embed a
// CDN-hosted library; that's intentional. Everything ships in the
// binary.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "0")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}

// sameOriginPOST enforces that POST requests carry an Origin header that
// matches the server's own Host, blocking simple cross-site POSTs from
// triggering state-changing operations (a best-effort CSRF defense for a
// tool that doesn't use cookies or auth tokens).
//
// Rules:
//   - Non-POST requests pass through unchanged.
//   - POST with no Origin AND no Referer is rejected (a browser always
//     sends at least one for cross-origin requests; a missing-both is
//     suspicious but does happen for some CLI tools that do send from
//     the dashboard origin, so we allow same-host Referer as fallback).
//   - POST with Origin must match r.Host.
//   - POST with only Referer must start with the same scheme://host.
//
// This is intentionally not a full CSRF token scheme — we don't manage
// sessions. The goal is to block the drive-by attack where a victim
// visits attacker.example while the dashboard is open in another tab.
func sameOriginPOST(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		referer := r.Header.Get("Referer")
		if origin == "" && referer == "" {
			http.Error(w, "missing Origin/Referer", http.StatusForbidden)
			return
		}
		if origin != "" {
			if !matchesHost(origin, r.Host) {
				http.Error(w, "cross-origin POST blocked", http.StatusForbidden)
				return
			}
		} else if !matchesHost(referer, r.Host) {
			http.Error(w, "cross-origin POST blocked", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// matchesHost checks whether a full URL (or origin) targets the given Host
// header value. Accepts both scheme://host[:port] and scheme://host[:port]/path.
func matchesHost(urlOrOrigin, host string) bool {
	// Strip scheme
	for _, scheme := range []string{"https://", "http://"} {
		if len(urlOrOrigin) > len(scheme) && urlOrOrigin[:len(scheme)] == scheme {
			urlOrOrigin = urlOrOrigin[len(scheme):]
			break
		}
	}
	// Take up to first slash
	if i := strings.IndexByte(urlOrOrigin, '/'); i >= 0 {
		urlOrOrigin = urlOrOrigin[:i]
	}
	return urlOrOrigin == host
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("POST /api/sync", s.handleSyncRun)
	mux.HandleFunc("GET /api/sync", s.handleSyncStatus)
	mux.HandleFunc("GET /api/i18n", s.handleI18n)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	// Order: securityHeaders outermost so every response (including the
	// CSRF 403) carries the hardening headers, then sameOriginPOST to
	// block cross-site state-changing requests, then the mux.
	return securityHeaders(sameOriginPOST(mux))
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

// maxRangeSeconds clamps untrusted ?range= values to our 7-day retention
// window. Anything longer is pointless (we don't keep older data) and
// anything larger than int32 risks time.Duration overflow.
const maxRangeSeconds = 7 * 24 * 3600

// parseRangeSeconds strictly parses a ?range=<integer-seconds> parameter.
// Returns (seconds, ok). Rejects:
//   - non-numeric input (no trailing garbage like "1abc")
//   - negative values
//   - zero
//   - values exceeding the 7-day retention window
//
// Uses strconv.Atoi (stricter than fmt.Sscanf which accepts trailing text).
func parseRangeSeconds(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	if n > maxRangeSeconds {
		n = maxRangeSeconds
	}
	return n, true
}

// handleMetrics returns metric points for the requested range.
// ?range=<seconds> — filters to the last N seconds at full resolution.
// Omitting range returns the full 7-day buffer sampled to 2016 points.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var points []MetricPoint
	if secs, ok := parseRangeSeconds(r.URL.Query().Get("range")); ok {
		points = s.scheduler.GetMetricsSince(time.Duration(secs)*time.Second, 2016)
	}
	if points == nil {
		points = s.scheduler.GetMetrics(2016)
	}

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
// ?range=<seconds> — returns entries from the last N seconds.
// Omitting range returns all in-memory entries (up to 7 days).
// All reads are pure in-memory operations (no disk I/O per request).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store")

	if s.logWatcher == nil {
		json.NewEncoder(w).Encode(map[string]any{"entries": []any{}})
		return
	}

	var entries []LogEntry
	if secs, ok := parseRangeSeconds(r.URL.Query().Get("range")); ok {
		entries = s.logWatcher.GetEntriesSince(time.Duration(secs) * time.Second)
	}
	if entries == nil {
		entries = s.logWatcher.GetEntries()
	}

	// Return in reverse chronological order (newest first)
	reversed := make([]LogEntry, len(entries))
	for i, e := range entries {
		reversed[len(entries)-1-i] = e
	}
	json.NewEncoder(w).Encode(map[string]any{"entries": reversed})
}

// handleSyncRun runs a full sync against authoritative log sources and
// returns the resulting SyncResult. Single-flight: if a sync is already
// running, returns 409 Conflict.
//
// POST /api/sync
//
// Response codes:
//   200 — sync completed (possibly with per-chunk errors in result.errors)
//   409 — a sync is already in progress
//   501 — journalctl not available on this system
//   500 — unexpected error starting the sync
func (s *Server) handleSyncRun(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store")

	if s.syncer == nil || !s.syncer.Enabled() {
		w.WriteHeader(http.StatusNotImplemented)
		json.NewEncoder(w).Encode(map[string]any{
			"error": "sync unavailable",
		})
		return
	}

	result, err := s.syncer.Sync(r.Context())
	if err != nil {
		// Never leak raw err.Error() to HTTP clients — it may contain
		// file paths, subprocess details, DB DSNs, or other internal
		// state. Log the full error server-side, return a short
		// category label to the client.
		var status int
		var msg string
		switch err {
		case ErrSyncInProgress:
			status, msg = http.StatusConflict, "sync already in progress"
		case ErrJournalctlUnavailable:
			status, msg = http.StatusNotImplemented, "sync unavailable"
		default:
			status, msg = http.StatusInternalServerError, "sync failed"
			s.logger.Warn("sync failed", "error", err)
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": msg})
		return
	}
	json.NewEncoder(w).Encode(result)
}

// handleSyncStatus returns last sync time and in-flight flag.
//
// GET /api/sync
func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store")

	if s.syncer == nil || !s.syncer.Enabled() {
		json.NewEncoder(w).Encode(map[string]any{
			"enabled": false,
		})
		return
	}
	last := s.syncer.LastSync()
	var lastStr any
	if !last.IsZero() {
		lastStr = last.Format(time.RFC3339)
	}
	json.NewEncoder(w).Encode(map[string]any{
		"enabled":   true,
		"last_sync": lastStr,
		"in_flight": s.syncer.InFlight(),
		"result":    s.syncer.LastResult(),
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	passing := s.scheduler.CriticalChecksPassing(s.config.Healthz.CriticalChecks)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	// Use json.NewEncoder to correctly escape any characters in the
	// translated status string. Hand-concatenating JSON with raw i18n
	// values would be fragile if a translator ever introduced a quote
	// or control character.
	var status string
	if passing {
		status = s.tr.T("status.healthy")
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		status = s.tr.T("status.unhealthy")
	}
	json.NewEncoder(w).Encode(map[string]string{"status": status})
}
