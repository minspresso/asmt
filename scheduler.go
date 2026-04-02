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
}

func NewScheduler(checkers []Checker, interval time.Duration, alerter Alerter, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		checkers:       checkers,
		results:        make(map[string][]CheckResult),
		previousStatus: make(map[string]Status),
		interval:       interval,
		alerter:        alerter,
		logger:         logger,
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

			// Set duration on all results
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

	for cr := range ch {
		s.mu.Lock()
		s.results[cr.name] = cr.results

		// Check for status transitions and alert
		for _, r := range cr.results {
			prevStatus, exists := s.previousStatus[r.Component]
			if !exists {
				s.previousStatus[r.Component] = r.Status
				if r.Status == StatusCritical || r.Status == StatusWarn {
					s.alert(ctx, r, StatusUnknown)
				}
				continue
			}

			if r.Status != prevStatus {
				s.alert(ctx, r, prevStatus)
				s.previousStatus[r.Component] = r.Status
			}
		}
		s.mu.Unlock()
	}
}

func (s *Scheduler) alert(ctx context.Context, result CheckResult, prevStatus Status) {
	if s.alerter == nil {
		return
	}
	if err := s.alerter.Alert(ctx, result, prevStatus); err != nil {
		s.logger.Error("alert failed", "component", result.Component, "error", err)
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
func (s *Scheduler) OverallStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

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

// CriticalChecksPassing returns true if all checks in the given list are OK or Warn.
func (s *Scheduler) CriticalChecksPassing(names []string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}

	for name, results := range s.results {
		if !nameSet[name] {
			continue
		}
		for _, r := range results {
			if r.Status == StatusCritical {
				return false
			}
		}
	}
	return true
}
