// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso
//
// Benchmark tests for the aggregating logBuffer under high sustained load.
// These verify the claim that we can handle hundreds of events/second for
// extended periods without memory growth — aggregation collapses events to
// O(unique buckets × titles).

package main

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// TestLogBufferHighThroughput simulates sustained load to verify that
// memory stays bounded by aggregation dimensions, not by event rate.
//
// We push 500 events/second × 10 simulated hours × 20 distinct error types
// = 36 million events (scaled down for test speed to 1M events, same
// aggregation math applies). Memory should be bounded by the number of
// distinct (15-min bucket × title × source) combinations.
func TestLogBufferHighThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping high-throughput test in short mode")
	}

	buf := newLogBuffer(7*24*time.Hour, 20000)

	const (
		eventsPerSecond = 500
		durationSec     = 60 // simulate 60 wall-clock seconds of traffic
		distinctTitles  = 20
	)
	totalEvents := eventsPerSecond * durationSec

	// Capture starting memory.
	var memStart runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memStart)

	start := time.Now()
	// Distribute events over a 1-hour window so they land in 4 different
	// 15-min buckets (matches real sustained-load behaviour).
	windowStart := time.Now().Add(-1 * time.Hour)
	for i := 0; i < totalEvents; i++ {
		buf.AddEvent(LogEntry{
			// Spread timestamps across the 1-hour window.
			Timestamp: windowStart.Add(time.Duration(i) * time.Hour / time.Duration(totalEvents)),
			Source:    "test",
			Severity:  "warn",
			Line:      fmt.Sprintf("test event %d", i%distinctTitles),
			Title:     fmt.Sprintf("title-%d", i%distinctTitles),
		})
	}
	elapsed := time.Since(start)

	var memEnd runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memEnd)

	bufLen := buf.Len()
	throughput := float64(totalEvents) / elapsed.Seconds()
	// Signed delta so we can see shrinkage (common after aggregation+GC).
	memGrowthMB := (int64(memEnd.HeapAlloc) - int64(memStart.HeapAlloc)) / (1024 * 1024)

	t.Logf("ingested %d events in %s (%.0f events/sec)", totalEvents, elapsed, throughput)
	t.Logf("buffer length after: %d entries (distinct titles=%d)", bufLen, distinctTitles)
	t.Logf("heap growth: %d MB (negative = shrunk after GC)", memGrowthMB)

	// Aggregation should collapse 30,000 events to at most a handful of
	// buckets × 20 titles. A 1-hour span can straddle up to 5 bucket
	// boundaries if aligned poorly, giving 5×20 = 100 entries max.
	maxExpected := 5 * distinctTitles
	if bufLen > maxExpected {
		t.Errorf("buffer grew to %d entries, expected ≤ %d (aggregation failure)", bufLen, maxExpected)
	}

	// Throughput sanity: we should handle at least 10x the target rate.
	minThroughput := float64(eventsPerSecond * 10)
	if throughput < minThroughput {
		t.Errorf("throughput %.0f events/sec below expected %.0f", throughput, minThroughput)
	}

	// Memory growth should be small — aggregation keeps it bounded.
	if memGrowthMB > 20 {
		t.Errorf("heap grew by %d MB, expected <20 MB", memGrowthMB)
	}
}

// TestLogBufferSustained24h verifies the exact claim: 500 events/second
// sustained for 24 hours = 43.2 million events. Memory must stay bounded
// and throughput must stay high.
func TestLogBufferSustained24h(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 24-hour sustained test in short mode")
	}

	buf := newLogBuffer(7*24*time.Hour, 20000)

	const (
		eventsPerSecond = 500
		simulatedHours  = 24
		distinctTitles  = 20 // realistic number of unique error patterns
	)
	totalEvents := eventsPerSecond * simulatedHours * 3600 // 43.2M events

	var memStart runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memStart)

	start := time.Now()
	windowStart := time.Now().Add(-time.Duration(simulatedHours) * time.Hour)
	windowSpan := time.Duration(simulatedHours) * time.Hour
	// Step per event as Duration — avoids int64 overflow that happens
	// when you multiply totalEvents by windowSpan nanoseconds directly.
	step := windowSpan / time.Duration(totalEvents)

	for i := 0; i < totalEvents; i++ {
		// Distribute timestamps uniformly across the 24h window.
		offset := step * time.Duration(i)
		buf.AddEvent(LogEntry{
			Timestamp: windowStart.Add(offset),
			Source:    "test",
			Severity:  "warn",
			Line:      fmt.Sprintf("event %d", i%distinctTitles),
			Title:     fmt.Sprintf("title-%d", i%distinctTitles),
		})
	}
	elapsed := time.Since(start)

	var memEnd runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memEnd)

	bufLen := buf.Len()
	throughput := float64(totalEvents) / elapsed.Seconds()
	memGrowthMB := (int64(memEnd.HeapAlloc) - int64(memStart.HeapAlloc)) / (1024 * 1024)
	// Peak heap during the run is more interesting than final (after GC).
	peakHeapMB := int64(memEnd.Sys) / (1024 * 1024)

	t.Logf("=== 24-HOUR SUSTAINED LOAD TEST ===")
	t.Logf("total events:      %d (%.1fM)", totalEvents, float64(totalEvents)/1e6)
	t.Logf("elapsed:           %s", elapsed)
	t.Logf("throughput:        %.0f events/sec", throughput)
	t.Logf("buffer length:     %d aggregated entries", bufLen)
	t.Logf("heap growth:       %d MB", memGrowthMB)
	t.Logf("runtime total Sys: %d MB", peakHeapMB)

	// Expected aggregation: 24h / 15min = 96 buckets, × 20 titles = 1920 max.
	maxExpected := 96*distinctTitles + 20 // slack for boundary alignment
	if bufLen > maxExpected {
		t.Errorf("buffer grew to %d entries, expected ≤ %d", bufLen, maxExpected)
	}

	// Sanity: throughput must exceed the target by a healthy margin.
	if throughput < 10000 {
		t.Errorf("throughput %.0f events/sec too slow (need >10k)", throughput)
	}

	// Heap growth should stay well under our GOMEMLIMIT of 64 MiB.
	if memGrowthMB > 50 {
		t.Errorf("heap grew by %d MB, expected <50 MB", memGrowthMB)
	}
}

// TestLogBufferConcurrent verifies aggregation is safe under concurrent
// writes from multiple goroutines (real scenario: tail goroutines + sync).
func TestLogBufferConcurrent(t *testing.T) {
	buf := newLogBuffer(7*24*time.Hour, 20000)

	const (
		goroutines      = 8
		eventsPerWorker = 10000
		distinctTitles  = 10
	)

	done := make(chan struct{}, goroutines)
	start := time.Now()
	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < eventsPerWorker; i++ {
				buf.AddEvent(LogEntry{
					Timestamp: time.Now().Add(-time.Duration(i) * time.Millisecond),
					Source:    "test",
					Severity:  "warn",
					Line:      fmt.Sprintf("worker %d event %d", gid, i),
					Title:     fmt.Sprintf("title-%d", i%distinctTitles),
				})
			}
		}(g)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
	elapsed := time.Since(start)
	total := goroutines * eventsPerWorker
	t.Logf("concurrent: %d events in %s across %d goroutines (%.0f events/sec)",
		total, elapsed, goroutines, float64(total)/elapsed.Seconds())
	t.Logf("buffer length after: %d entries", buf.Len())

	// Verify sum of all Counts equals total events added.
	entries := buf.Entries(0)
	sum := 0
	for _, e := range entries {
		sum += e.Count
	}
	if sum != total {
		t.Errorf("aggregation count mismatch: sum=%d, expected=%d", sum, total)
	}
}
