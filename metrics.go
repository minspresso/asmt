// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// metricsMaxPoints holds 7 days of readings at 30s intervals.
const metricsMaxPoints = 20160

// MetricPoint is a single timestamped memory + load reading.
// Kept small: 13 bytes per point × 20160 = ~260 KB worst case.
type MetricPoint struct {
	T int64   `json:"t"` // unix timestamp (seconds)
	M uint8   `json:"m"` // memory usage %
	L float32 `json:"l"` // load average 1m
}

// MetricsBuffer is a thread-safe ring buffer of MetricPoints.
// It reuses the HistoryStore's directory for file persistence.
type MetricsBuffer struct {
	mu     sync.RWMutex
	points []MetricPoint
	store  *HistoryStore
	saveN  int // readings since last disk write
}

func NewMetricsBuffer(store *HistoryStore) *MetricsBuffer {
	mb := &MetricsBuffer{store: store}
	if store != nil {
		mb.load()
	}
	return mb
}

// Push adds a reading. Persists today's data every 20 pushes (~10 minutes).
func (mb *MetricsBuffer) Push(p MetricPoint) {
	mb.mu.Lock()
	mb.points = append(mb.points, p)
	if len(mb.points) > metricsMaxPoints {
		mb.points = mb.points[len(mb.points)-metricsMaxPoints:]
	}
	mb.saveN++
	shouldSave := mb.saveN >= 20
	var toSave []MetricPoint
	if shouldSave {
		mb.saveN = 0
		toSave = mb.todayPoints()
	}
	mb.mu.Unlock()

	if shouldSave && mb.store != nil {
		mb.saveToday(toSave)
	}
}

// Since returns points from the last d duration, sampled to at most maxPoints.
// Unlike Get, it filters by time first so short ranges get full resolution.
func (mb *MetricsBuffer) Since(d time.Duration, maxPoints int) []MetricPoint {
	mb.mu.RLock()
	defer mb.mu.RUnlock()

	cutoff := time.Now().Unix() - int64(d.Seconds())
	start := len(mb.points)
	for i, p := range mb.points {
		if p.T >= cutoff {
			start = i
			break
		}
	}
	if start >= len(mb.points) {
		return nil
	}
	pts := mb.points[start:]
	if maxPoints <= 0 || len(pts) <= maxPoints {
		out := make([]MetricPoint, len(pts))
		copy(out, pts)
		return out
	}
	step := float64(len(pts)) / float64(maxPoints)
	out := make([]MetricPoint, 0, maxPoints)
	for i := 0; i < maxPoints; i++ {
		out = append(out, pts[int(float64(i)*step)])
	}
	return out
}

// Get returns up to maxPoints evenly sampled from the full buffer.
func (mb *MetricsBuffer) Get(maxPoints int) []MetricPoint {
	mb.mu.RLock()
	defer mb.mu.RUnlock()

	pts := mb.points
	if len(pts) == 0 {
		return nil
	}
	if maxPoints <= 0 || len(pts) <= maxPoints {
		out := make([]MetricPoint, len(pts))
		copy(out, pts)
		return out
	}

	step := float64(len(pts)) / float64(maxPoints)
	out := make([]MetricPoint, 0, maxPoints)
	for i := 0; i < maxPoints; i++ {
		out = append(out, pts[int(float64(i)*step)])
	}
	return out
}

// todayPoints returns only today's points. Must be called with mu held.
func (mb *MetricsBuffer) todayPoints() []MetricPoint {
	today := time.Now().UTC().Truncate(24 * time.Hour).Unix()
	var out []MetricPoint
	for _, p := range mb.points {
		if p.T >= today {
			out = append(out, p)
		}
	}
	return out
}

func (mb *MetricsBuffer) saveToday(points []MetricPoint) {
	if mb.store == nil || len(points) == 0 {
		return
	}
	if err := os.MkdirAll(mb.store.dir, 0755); err != nil {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	path := filepath.Join(mb.store.dir, "metrics-"+today+".json")
	tmp := path + ".tmp"

	data, err := json.Marshal(struct {
		MachineID string        `json:"machine_id"`
		Points    []MetricPoint `json:"points"`
	}{MachineID: mb.store.machineID, Points: points})
	if err != nil {
		return
	}
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, path)
}

// load reads the last 7 days of metric files into the buffer on startup.
func (mb *MetricsBuffer) load() {
	if mb.store == nil {
		return
	}
	entries, err := os.ReadDir(mb.store.dir)
	if err != nil {
		return
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -7).Unix()
	var all []MetricPoint

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "metrics-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(mb.store.dir, name))
		if err != nil {
			continue
		}
		var f struct {
			MachineID string        `json:"machine_id"`
			Points    []MetricPoint `json:"points"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		// Skip files written by a different machine (e.g. data copied from old VM).
		// Files without a machine_id (written before this fix) are also skipped.
		if f.MachineID != mb.store.machineID {
			continue
		}
		for _, p := range f.Points {
			if p.T >= cutoff {
				all = append(all, p)
			}
		}
	}

	sort.Slice(all, func(i, j int) bool { return all[i].T < all[j].T })
	if len(all) > metricsMaxPoints {
		all = all[len(all)-metricsMaxPoints:]
	}
	mb.points = all
}
