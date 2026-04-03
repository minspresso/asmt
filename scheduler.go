// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// HistoryDay records the worst status seen for a component on a given UTC date.
type HistoryDay struct {
	Date   string `json:"date"`   // "2006-01-02"
	Status string `json:"status"` // "ok", "warn", "critical", "unknown"
}

type Scheduler struct {
	mu             sync.RWMutex
	checkers       []Checker
	results        map[string][]CheckResult
	previousStatus map[string]Status
	history        map[string][]HistoryDay // component -> up to 7 days, oldest first
	historyStore   *HistoryStore
	metrics        *MetricsBuffer
	interval       time.Duration
	alerter        Alerter
	logger         *slog.Logger
	tr             *Translations
}

func NewScheduler(checkers []Checker, interval time.Duration, alerter Alerter, logger *slog.Logger, tr *Translations, store *HistoryStore) *Scheduler {
	// Guard against zero/negative interval (would panic in NewTicker)
	if interval <= 0 {
		interval = 30 * time.Second
	}

	s := &Scheduler{
		checkers:       checkers,
		results:        make(map[string][]CheckResult),
		previousStatus: make(map[string]Status),
		history:        make(map[string][]HistoryDay),
		historyStore:   store,
		metrics:        NewMetricsBuffer(store),
		interval:       interval,
		alerter:        alerter,
		logger:         logger,
		tr:             tr,
	}

	// Seed in-memory history from disk on startup.
	if store != nil {
		for component, days := range store.Load() {
			s.history[component] = days
		}
	}

	return s
}

// historyPriority ranks statuses for worst-of-day tracking.
// Unknown means no data and loses to everything.
func historyPriority(s string) int {
	switch s {
	case "critical":
		return 3
	case "warn":
		return 2
	case "ok":
		return 1
	default:
		return 0
	}
}

// updateHistory records the worst status seen for a component today.
// Must be called with s.mu held for writing.
func (s *Scheduler) updateHistory(component string, status Status) {
	if status == StatusUnknown {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	days := s.history[component]

	if len(days) == 0 || days[len(days)-1].Date != today {
		days = append(days, HistoryDay{Date: today, Status: "unknown"})
		if len(days) > 7 {
			days = days[len(days)-7:]
		}
	}

	incoming := status.String()
	if historyPriority(incoming) > historyPriority(days[len(days)-1].Status) {
		days[len(days)-1].Status = incoming
	}
	s.history[component] = days
}

// GetHistory returns a 7-day history for every component, padded with
// "unknown" for any days that have no data yet.
func (s *Scheduler) GetHistory() map[string][]HistoryDay {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Pre-build the 7-day date window (oldest → newest).
	dates := make([]string, 7)
	now := time.Now().UTC()
	for i := 0; i < 7; i++ {
		dates[i] = now.AddDate(0, 0, -(6 - i)).Format("2006-01-02")
	}

	result := make(map[string][]HistoryDay, len(s.history))
	for component, recorded := range s.history {
		padded := make([]HistoryDay, 7)
		for i, d := range dates {
			padded[i] = HistoryDay{Date: d, Status: "unknown"}
		}
		for _, day := range recorded {
			for i, p := range padded {
				if p.Date == day.Date {
					padded[i].Status = day.Status
					break
				}
			}
		}
		result[component] = padded
	}
	return result
}

func (s *Scheduler) Start(ctx context.Context) {
	// Run immediately on start
	s.runAll(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runAll(ctx)
		}
	}
}

func (s *Scheduler) runAll(ctx context.Context) {
	var wg sync.WaitGroup

	type checkerResult struct {
		name    string
		results []CheckResult
	}

	ch := make(chan checkerResult, len(s.checkers))

	for _, checker := range s.checkers {
		wg.Add(1)
		go func(c Checker) {
			defer wg.Done()

			checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()

			start := time.Now()
			results := c.Check(checkCtx)
			duration := time.Since(start)

			for i := range results {
				results[i].Duration = duration
			}

			ch <- checkerResult{name: c.Name(), results: results}
		}(checker)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	// Collect alerts to send OUTSIDE the lock to avoid blocking reads
	type pendingAlert struct {
		result     CheckResult
		prevStatus Status
	}
	var alerts []pendingAlert

	for cr := range ch {
		s.mu.Lock()
		s.results[cr.name] = cr.results

		for _, r := range cr.results {
			s.updateHistory(r.Component, r.Status)

			prevStatus, exists := s.previousStatus[r.Component]
			if !exists {
				s.previousStatus[r.Component] = r.Status
				if r.Status == StatusCritical || r.Status == StatusWarn {
					alerts = append(alerts, pendingAlert{r, StatusUnknown})
				}
				continue
			}

			if r.Status != prevStatus {
				alerts = append(alerts, pendingAlert{r, prevStatus})
				s.previousStatus[r.Component] = r.Status
			}
		}
		s.mu.Unlock()
	}

	// Send alerts without holding the lock (prevents slow webhook/SMTP from blocking reads)
	for _, a := range alerts {
		s.alert(ctx, a.result, a.prevStatus)
	}

	// Persist today's history to disk outside the lock.
	if s.historyStore != nil {
		if err := s.historyStore.Save(s.GetHistory()); err != nil {
			s.logger.Warn("failed to persist history", "error", err)
		}
	}

	// Record a metrics point from the Linux checker results.
	s.mu.RLock()
	memResults := s.results["linux"]
	s.mu.RUnlock()
	if pt, ok := extractMetricPoint(memResults); ok {
		s.metrics.Push(pt)
	}
}

// extractMetricPoint pulls mem% and load_1m out of Linux checker results.
func extractMetricPoint(results []CheckResult) (MetricPoint, bool) {
	var mem uint8
	var load float32
	var hasMem, hasLoad bool

	for _, r := range results {
		if r.Component == "linux-memory" && r.Details != nil {
			if v, ok := r.Details["usage_pct"]; ok {
				var pct int
				if _, err := fmt.Sscanf(v, "%d", &pct); err == nil {
					mem = uint8(pct)
					hasMem = true
				}
			}
		}
		if r.Component == "linux-load" && r.Details != nil {
			if v, ok := r.Details["load_1m"]; ok {
				var l float32
				if _, err := fmt.Sscanf(v, "%f", &l); err == nil {
					load = l
					hasLoad = true
				}
			}
		}
	}

	if hasMem && hasLoad {
		return MetricPoint{T: time.Now().Unix(), M: mem, L: load}, true
	}
	return MetricPoint{}, false
}

func (s *Scheduler) alert(ctx context.Context, result CheckResult, prevStatus Status) {
	if s.alerter == nil {
		return
	}
	if err := s.alerter.Alert(ctx, result, prevStatus); err != nil {
		s.logger.Error(s.tr.T("server.alert_failed"), "component", result.Component, "error", err)
	}
}

// GetMetrics returns up to maxPoints evenly sampled metric readings.
func (s *Scheduler) GetMetrics(maxPoints int) []MetricPoint {
	return s.metrics.Get(maxPoints)
}

// GetStatus returns a snapshot of all current check results.
func (s *Scheduler) GetStatus() map[string][]CheckResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := make(map[string][]CheckResult, len(s.results))
	for k, v := range s.results {
		copied := make([]CheckResult, len(v))
		copy(copied, v)
		snapshot[k] = copied
	}
	return snapshot
}

// OverallStatus returns the worst status across all checks.
// Returns StatusUnknown if no checks have reported yet.
func (s *Scheduler) OverallStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.results) == 0 {
		return StatusUnknown
	}

	worst := StatusOK
	for _, results := range s.results {
		for _, r := range results {
			if r.Status > worst {
				worst = r.Status
			}
		}
	}
	return worst
}

// CriticalChecksPassing returns true only if ALL named checks have reported
// results and none are Critical. Returns false if any named check has not
// reported yet (unknown = not passing, to prevent routing traffic to
// unverified servers).
func (s *Scheduler) CriticalChecksPassing(names []string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, name := range names {
		results, exists := s.results[name]
		if !exists {
			return false // no data yet = not passing
		}
		for _, r := range results {
			if r.Status == StatusCritical {
				return false
			}
		}
	}
	return true
}
