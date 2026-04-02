// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Scheduler struct {
	mu             sync.RWMutex
	checkers       []Checker
	results        map[string][]CheckResult
	previousStatus map[string]Status
	interval       time.Duration
	alerter        Alerter
	logger         *slog.Logger
	tr             *Translations
}

func NewScheduler(checkers []Checker, interval time.Duration, alerter Alerter, logger *slog.Logger, tr *Translations) *Scheduler {
	// Guard against zero/negative interval (would panic in NewTicker)
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Scheduler{
		checkers:       checkers,
		results:        make(map[string][]CheckResult),
		previousStatus: make(map[string]Status),
		interval:       interval,
		alerter:        alerter,
		logger:         logger,
		tr:             tr,
	}
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
}

func (s *Scheduler) alert(ctx context.Context, result CheckResult, prevStatus Status) {
	if s.alerter == nil {
		return
	}
	if err := s.alerter.Alert(ctx, result, prevStatus); err != nil {
		s.logger.Error(s.tr.T("server.alert_failed"), "component", result.Component, "error", err)
	}
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
