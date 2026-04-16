// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LogSeverity indicates how serious a matched log pattern is.
type LogSeverity int

const (
	LogInfo LogSeverity = iota
	LogWarn
	LogError
)

func (s LogSeverity) String() string {
	switch s {
	case LogInfo:
		return "info"
	case LogWarn:
		return "warn"
	case LogError:
		return "error"
	default:
		return "info"
	}
}

// LogPattern defines a known error pattern and its mitigation advice.
type LogPattern struct {
	Substring   string
	Severity    LogSeverity
	Source      string // which service: nginx, php-fpm, mariadb, system
	TitleKey    string // i18n key for the pattern title
	MitigateKey string // i18n key for the mitigation advice
}

// LogEntry represents one aggregated "incident": all matches of the same
// error pattern within a single 15-minute bucket are rolled up into one entry.
// This keeps memory bounded regardless of error rate: a DDoS producing 10,000
// matches/second in a 15-min window still takes just one LogEntry.
//
//   - Timestamp: first occurrence time (stable, used for chronological sorting)
//   - LastSeen:  most recent occurrence time in this bucket
//   - Line:      sample log line (the first one observed, representative)
//   - Count:     total number of matches in this (bucket, title, source)
type LogEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	LastSeen   time.Time `json:"last_seen"`
	Source     string    `json:"source"`
	Severity   string    `json:"severity"`
	Line       string    `json:"line"`
	Title      string    `json:"title"`
	Mitigation string    `json:"mitigation"`
	Count      int       `json:"count"`
}

// logBucketSeconds is the 15-minute aggregation bucket width in seconds.
const logBucketSeconds = 900

// aggKey identifies one aggregated "incident": all matches of the same
// pattern from the same source within the same 15-minute bucket.
type aggKey struct {
	bucketStart int64 // unix seconds of 15-min bucket start (UTC)
	title       string
	source      string
}

func bucketKeyFor(ts time.Time, title, source string) aggKey {
	unix := ts.Unix()
	return aggKey{
		bucketStart: unix - (unix % logBucketSeconds),
		title:       title,
		source:      source,
	}
}

// logBuffer is a time-aware, aggregating in-memory store for log entries.
//
// Design goals:
//   - Memory is bounded by DIMENSIONS (time × error types), NOT by rate.
//     A DDoS producing 1M matches/sec contributes ~1 entry per unique error
//     per 15-min bucket, identical memory to a quiet server with the same
//     error variety.
//   - All entries from the last 7 days live in memory so range queries
//     are pure in-memory operations (no disk reads, no JSON parsing).
//   - AddEvent: O(1) amortised map lookup + append in the common case.
//   - Range queries: O(log n) binary search + slice copy.
//
// Internals:
//   - entries: slice of *LogEntry sorted by first-seen Timestamp.
//     Pointers (not values) so the map and the slice share the same
//     LogEntry instance, updating a count via the map is visible via
//     the slice without a second lookup.
//   - keyed: map from aggKey to *LogEntry for O(1) aggregation lookups.
type logBuffer struct {
	mu      sync.RWMutex
	entries []*LogEntry          // sorted by Timestamp (first-seen), oldest first
	keyed   map[aggKey]*LogEntry // for O(1) aggregation lookup
	maxAge  time.Duration        // entries older than this are pruned
	maxSize int                  // hard cap on number of aggregated entries
	addN    int                  // adds since last prune
}

func newLogBuffer(maxAge time.Duration, maxSize int) *logBuffer {
	return &logBuffer{
		entries: make([]*LogEntry, 0, 512),
		keyed:   make(map[aggKey]*LogEntry, 512),
		maxAge:  maxAge,
		maxSize: maxSize,
	}
}

// severityRank orders severities so higher = worse. Used to upgrade
// an aggregated entry's severity if a worse event arrives later in
// the same bucket (e.g., disk goes warn → critical mid-bucket).
func severityRank(s string) int {
	switch s {
	case "error":
		return 3
	case "warn":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

// AddEvent records a single matched log line. If another match for the same
// (15-min bucket, title, source) already exists, it is aggregated: the
// existing entry's count is incremented, its LastSeen is updated, and its
// severity is upgraded if the new event is worse. Otherwise a new entry is
// inserted in chronological order.
func (lb *logBuffer) AddEvent(e LogEntry) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	key := bucketKeyFor(e.Timestamp, e.Title, e.Source)
	if existing, ok := lb.keyed[key]; ok {
		existing.Count++
		if e.Timestamp.After(existing.LastSeen) {
			existing.LastSeen = e.Timestamp
		}
		// Upgrade to the worse severity if the new event is more serious.
		if severityRank(e.Severity) > severityRank(existing.Severity) {
			existing.Severity = e.Severity
			existing.Line = e.Line // update sample to the worse one
			if e.Mitigation != "" {
				existing.Mitigation = e.Mitigation
			}
		}
		return
	}

	// New aggregated entry: count starts at 1.
	entry := e
	entry.Count = 1
	entry.LastSeen = e.Timestamp
	entryPtr := &entry
	lb.keyed[key] = entryPtr

	// Insert in sorted order (by first-seen Timestamp).
	n := len(lb.entries)
	if n == 0 || !entry.Timestamp.Before(lb.entries[n-1].Timestamp) {
		// Fast path: append (common case for tail goroutines).
		lb.entries = append(lb.entries, entryPtr)
	} else {
		// Slow path: binary search + insert.
		idx := sort.Search(n, func(i int) bool {
			return lb.entries[i].Timestamp.After(entry.Timestamp)
		})
		lb.entries = append(lb.entries, nil)
		copy(lb.entries[idx+1:], lb.entries[idx:])
		lb.entries[idx] = entryPtr
	}

	lb.addN++
	if lb.addN >= 64 {
		lb.addN = 0
		lb.pruneLocked()
	}
}

// AddAggregated inserts a pre-aggregated entry loaded from disk.
// If the same (bucket, title, source) key already exists (e.g., from a
// duplicate disk file), counts are merged.
func (lb *logBuffer) AddAggregated(e LogEntry) {
	if e.Count < 1 {
		e.Count = 1 // backward compat for entries saved before aggregation
	}
	if e.LastSeen.IsZero() {
		e.LastSeen = e.Timestamp
	}

	lb.mu.Lock()
	defer lb.mu.Unlock()

	key := bucketKeyFor(e.Timestamp, e.Title, e.Source)
	if existing, ok := lb.keyed[key]; ok {
		existing.Count += e.Count
		if e.LastSeen.After(existing.LastSeen) {
			existing.LastSeen = e.LastSeen
		}
		return
	}

	entry := e
	entryPtr := &entry
	lb.keyed[key] = entryPtr

	n := len(lb.entries)
	if n == 0 || !entry.Timestamp.Before(lb.entries[n-1].Timestamp) {
		lb.entries = append(lb.entries, entryPtr)
	} else {
		idx := sort.Search(n, func(i int) bool {
			return lb.entries[i].Timestamp.After(entry.Timestamp)
		})
		lb.entries = append(lb.entries, nil)
		copy(lb.entries[idx+1:], lb.entries[idx:])
		lb.entries[idx] = entryPtr
	}
}

// pruneLocked drops entries older than maxAge and enforces maxSize.
// Both the slice and the keyed map are kept in sync. Caller must hold lb.mu.
func (lb *logBuffer) pruneLocked() {
	dropFromFront := 0

	// Age-based prune: entries are sorted, so binary search the cutoff.
	if lb.maxAge > 0 {
		cutoff := time.Now().Add(-lb.maxAge)
		dropFromFront = sort.Search(len(lb.entries), func(i int) bool {
			return !lb.entries[i].Timestamp.Before(cutoff)
		})
	}

	// Hard size cap: if still too big, drop more from the oldest end.
	if lb.maxSize > 0 && len(lb.entries)-dropFromFront > lb.maxSize {
		dropFromFront = len(lb.entries) - lb.maxSize
	}

	if dropFromFront > 0 {
		for i := 0; i < dropFromFront; i++ {
			dropped := lb.entries[i]
			key := bucketKeyFor(dropped.Timestamp, dropped.Title, dropped.Source)
			delete(lb.keyed, key)
			lb.entries[i] = nil // help GC
		}
		lb.entries = lb.entries[dropFromFront:]
	}
}

// copyRange returns a value-copy of entries in the given slice range.
// Caller must hold lb.mu (read or write lock).
func copyRange(src []*LogEntry) []LogEntry {
	out := make([]LogEntry, len(src))
	for i, p := range src {
		out[i] = *p
	}
	return out
}

// Entries returns up to `limit` of the newest entries in chronological order
// (oldest first). If limit <= 0, returns all entries.
func (lb *logBuffer) Entries(limit int) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	n := len(lb.entries)
	start := 0
	if limit > 0 && n > limit {
		start = n - limit
	}
	return copyRange(lb.entries[start:])
}

// EntriesSince returns all entries whose first-seen Timestamp is within the
// last `d` duration. Uses binary search for O(log n) cutoff lookup.
func (lb *logBuffer) EntriesSince(d time.Duration) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	cutoff := time.Now().Add(-d)
	idx := sort.Search(len(lb.entries), func(i int) bool {
		return !lb.entries[i].Timestamp.Before(cutoff)
	})
	return copyRange(lb.entries[idx:])
}

// EntriesForDay returns entries whose first-seen Timestamp falls on the given
// UTC day. Used by saveLogs for daily persistence.
func (lb *logBuffer) EntriesForDay(dayStart time.Time) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	dayEnd := dayStart.Add(24 * time.Hour)
	lo := sort.Search(len(lb.entries), func(i int) bool {
		return !lb.entries[i].Timestamp.Before(dayStart)
	})
	hi := sort.Search(len(lb.entries), func(i int) bool {
		return !lb.entries[i].Timestamp.Before(dayEnd)
	})
	return copyRange(lb.entries[lo:hi])
}

// MaxLastSeen returns the most recent LastSeen timestamp across all entries,
// or zero if the buffer is empty. Used by scanRecent to avoid re-counting
// events that were already loaded from disk.
func (lb *logBuffer) MaxLastSeen() time.Time {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	var max time.Time
	for _, e := range lb.entries {
		if e.LastSeen.After(max) {
			max = e.LastSeen
		}
	}
	return max
}

// Len returns the current aggregated entry count.
func (lb *logBuffer) Len() int {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return len(lb.entries)
}

// LogWatcher tails multiple log files and matches lines against known patterns.
type LogWatcher struct {
	patterns []LogPattern
	buffer   *logBuffer
	files    []LogFileConfig
	tr       *Translations
	store    *HistoryStore
	saveN    atomic.Int32 // entries since last persist (accessed from multiple goroutines)
	saveMu   sync.Mutex   // serialises saveLogs calls
}

// LogFileConfig defines a log file to watch.
type LogFileConfig struct {
	Path   string
	Source string // nginx, php-fpm, mariadb, system
}

// Buffer sizing:
//   - maxAge=7d matches our on-disk retention.
//   - maxSize caps the number of AGGREGATED entries, not raw events.
//     Each entry represents one (15-min bucket × title × source) combination.
//     Dimensions: 672 buckets (7d × 96/day) × realistic max ~25 distinct
//     error types per bucket = ~16800 entries worst case.
//   - Memory: 20000 entries × ~700 bytes ≈ 14MB hard ceiling.
//     Typical server: <2000 entries, <2MB.
//     A DDoS producing 1M matches/sec contributes the same memory as a
//     quiet server with the same variety of errors. Only count increases.
const (
	logBufferMaxAge  = 7 * 24 * time.Hour
	logBufferMaxSize = 20000
)

func NewLogWatcher(files []LogFileConfig, patterns []LogPattern, bufSize int, tr *Translations, store *HistoryStore) *LogWatcher {
	// bufSize from config is used as a soft hint; the real cap is logBufferMaxSize.
	// This keeps config.yaml compatible while preventing undersized buffers.
	maxSize := logBufferMaxSize
	if bufSize > 0 && bufSize > maxSize {
		maxSize = bufSize
	}
	lw := &LogWatcher{
		patterns: patterns,
		buffer:   newLogBuffer(logBufferMaxAge, maxSize),
		files:    files,
		tr:       tr,
		store:    store,
	}
	lw.loadLogs()
	return lw
}

// Start scans recent history from log files, then begins tailing for new lines.
func (lw *LogWatcher) Start(ctx context.Context) {
	lw.scanRecent()
	// Re-save after startup so any disk files loaded in a legacy (pre-aggregation)
	// format get rewritten in the compact aggregated format. Idempotent and cheap.
	if lw.buffer.Len() > 0 {
		lw.saveLogs()
	}
	for _, fc := range lw.files {
		go lw.tailFile(ctx, fc)
	}
}

// scanRecent reads the tail of each log file (up to 64KB) and matches
// patterns against existing lines. This captures incidents that happened
// before the process started.
//
// Cutoff logic: we take the MAX of (now-24h) and (MaxLastSeen from already-
// loaded buffer entries) + 1 second. This prevents double-counting events
// that were already persisted to disk and loaded via loadLogs.
func (lw *LogWatcher) scanRecent() {
	cutoff := time.Now().Add(-24 * time.Hour)
	if maxLoaded := lw.buffer.MaxLastSeen(); !maxLoaded.IsZero() {
		advanced := maxLoaded.Add(time.Second)
		if advanced.After(cutoff) {
			cutoff = advanced
		}
	}
	added := 0
	for _, fc := range lw.files {
		added += lw.scanFile(fc, cutoff)
	}
	if added > 0 {
		lw.saveLogs()
	}
}

const scanTailBytes = 64 * 1024 // 64KB per file

// scanFile reads the tail of a log file and feeds matching lines to the
// buffer via AddEvent. Aggregation in the buffer handles dedup naturally:
// the same (bucket, title, source) key just has its count bumped.
// Returns the number of matched lines processed.
func (lw *LogWatcher) scanFile(fc LogFileConfig, cutoff time.Time) int {
	f, err := os.Open(fc.Path)
	if err != nil {
		return 0
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0
	}

	readFrom := int64(0)
	if info.Size() > scanTailBytes {
		readFrom = info.Size() - scanTailBytes
	}
	if _, err := f.Seek(readFrom, io.SeekStart); err != nil {
		return 0
	}

	buf := make([]byte, info.Size()-readFrom)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return 0
	}
	buf = buf[:n]

	lines := strings.Split(string(buf), "\n")
	// If we seeked into the middle of the file, drop the first partial line.
	if readFrom > 0 && len(lines) > 0 {
		lines = lines[1:]
	}

	count := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ts := parseLogTimestamp(line)
		if ts.IsZero() || ts.Before(cutoff) {
			continue
		}
		if entry, ok := lw.matchLineEntry(line, fc.Source, ts); ok {
			lw.buffer.AddEvent(entry)
			count++
		}
	}
	return count
}

// parseLogTimestamp extracts a timestamp from common log formats.
func parseLogTimestamp(line string) time.Time {
	// Nginx: 2026/04/06 15:10:23
	if len(line) > 19 {
		if t, err := time.Parse("2006/01/02 15:04:05", line[:19]); err == nil {
			return t
		}
	}
	// PHP-FPM: [06-Apr-2026 15:35:33]
	if idx := strings.IndexByte(line, '['); idx >= 0 {
		end := strings.IndexByte(line[idx:], ']')
		if end > 0 {
			s := line[idx+1 : idx+end]
			if t, err := time.Parse("02-Jan-2006 15:04:05", s); err == nil {
				return t
			}
		}
	}
	// Syslog: "Apr  6 15:10:23". Go's "Jan  2" handles both space-padded and zero-padded days.
	if len(line) > 15 {
		if t, err := time.Parse("Jan  2 15:04:05", line[:15]); err == nil {
			t = t.AddDate(time.Now().Year(), 0, 0)
			return t
		}
	}
	return time.Time{}
}

// matchLineEntry returns a LogEntry if the line matches a known pattern.
// Does not add to the buffer. Caller decides what to do with the result.
// Used by scanFileBatch for bulk-loading with dedup.
func (lw *LogWatcher) matchLineEntry(line, source string, ts time.Time) (LogEntry, bool) {
	for _, p := range lw.patterns {
		if p.Source != "" && p.Source != source {
			continue
		}
		if strings.Contains(line, p.Substring) {
			return LogEntry{
				Timestamp:  ts,
				Source:     source,
				Severity:   p.Severity.String(),
				Line:       truncate(line, 500),
				Title:      lw.tr.T(p.TitleKey),
				Mitigation: lw.tr.T(p.MitigateKey),
			}, true
		}
	}
	return LogEntry{}, false
}

// GetEntries returns all buffered log entries.
// GetEntries returns up to `limit` of the most recent buffered entries in
// chronological order (oldest first). Pass 0 for all entries.
func (lw *LogWatcher) GetEntries() []LogEntry {
	return lw.buffer.Entries(0)
}

// RecordCheckResult records a check result (warn/critical) as a log entry.
// This lets the "Log Warnings" section show a unified timeline of both
// log-file pattern matches AND internal check state (disk, memory, load,
// DB ping, SSL cert expiry, etc.), anything tracked by the scheduler.
//
// OK results are ignored. Aggregation handles high-frequency updates: a
// check that's warn for an entire 15-min window becomes one entry with a
// count reflecting how many check cycles it was bad.
func (lw *LogWatcher) RecordCheckResult(r CheckResult) {
	if lw == nil {
		return
	}
	if r.Status != StatusWarn && r.Status != StatusCritical {
		return
	}
	severity := "warn"
	if r.Status == StatusCritical {
		severity = "error"
	}
	// Title = component name (e.g. "linux-disk", "mariadb-ping").
	// Source = "check" so UI chips show "check · linux-disk ×N".
	// Line = the check message (e.g. "/var: 86% used").
	// Mitigation is empty. The check itself already describes the issue.
	ts := r.CheckedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	lw.buffer.AddEvent(LogEntry{
		Timestamp: ts,
		Source:    "check",
		Severity:  severity,
		Line:      r.Message,
		Title:     r.Component,
	})
	if lw.saveN.Add(1) >= 10 {
		lw.saveN.Store(0)
		lw.saveLogs()
	}
}

// GetEntriesSince returns log entries from the last d duration.
// Pure in-memory operation, no disk reads. O(log n + k) where k is result size.
func (lw *LogWatcher) GetEntriesSince(d time.Duration) []LogEntry {
	return lw.buffer.EntriesSince(d)
}

// tailFile opens a log file, seeks to the end, and reads new lines as they appear.
// Uses polling with raw reads instead of bufio.Scanner (which caches EOF).
func (lw *LogWatcher) tailFile(ctx context.Context, fc LogFileConfig) {
	for {
		if err := lw.tailOnce(ctx, fc); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				continue
			}
		}
	}
}

func (lw *LogWatcher) tailOnce(ctx context.Context, fc LogFileConfig) error {
	f, err := os.Open(fc.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	// Seek to end - only process new lines
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Reusable read buffer (4KB max per read, bounded memory)
	readBuf := make([]byte, 4096)
	// Partial line leftover from previous read
	var partial string

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Check if file was rotated (size shrank or inode changed)
			info, err := os.Stat(fc.Path)
			if err != nil {
				return err
			}
			if info.Size() < offset {
				return nil // file rotated, reopen
			}

			// Read new bytes from current offset
			for {
				n, err := f.Read(readBuf)
				if n > 0 {
					offset += int64(n)
					chunk := partial + string(readBuf[:n])
					partial = ""

					lines := strings.Split(chunk, "\n")
					// Last element may be a partial line (no trailing newline yet)
					if !strings.HasSuffix(chunk, "\n") {
						partial = lines[len(lines)-1]
						lines = lines[:len(lines)-1]
					}

					for _, line := range lines {
						line = strings.TrimSpace(line)
						if line != "" {
							lw.matchLine(line, fc.Source)
						}
					}
				}
				if err != nil {
					break // EOF or error, wait for next tick
				}
			}
		}
	}
}

// matchLine is called by tail goroutines for each new log line.
// Uses time.Now() as the timestamp (monotonic, fast-path in buffer).
// AddEvent aggregates by (bucket, title, source), so chatty errors
// increment a counter instead of filling the buffer.
func (lw *LogWatcher) matchLine(line, source string) {
	entry, ok := lw.matchLineEntry(line, source, time.Now())
	if !ok {
		return
	}
	lw.buffer.AddEvent(entry)
	// Persist every 10 events (~infrequent writes).
	if lw.saveN.Add(1) >= 10 {
		lw.saveN.Store(0)
		lw.saveLogs()
	}
}

// truncate limits string length to avoid storing huge lines.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// logDayFile is the on-disk format for one day of log entries.
type logDayFile struct {
	MachineID string     `json:"machine_id"`
	Entries   []LogEntry `json:"entries"`
}

const logRetentionDays = 7

// saveLogs persists today's log entries to disk (atomic write).
// saveLogs persists today's buffered entries to disk (atomic write).
// Uses EntriesForDay which binary-searches the day's slice in O(log n).
func (lw *LogWatcher) saveLogs() {
	if lw.store == nil {
		return
	}
	lw.saveMu.Lock()
	defer lw.saveMu.Unlock()
	// 0750 dir, 0640 file. Log entries may contain sample lines from
	// /var/log files that are typically root-readable only.
	if err := os.MkdirAll(lw.store.dir, 0750); err != nil {
		return
	}

	todayStart := time.Now().UTC().Truncate(24 * time.Hour)
	todayEntries := lw.buffer.EntriesForDay(todayStart)
	if len(todayEntries) == 0 {
		return
	}

	data, err := json.Marshal(logDayFile{
		MachineID: lw.store.machineID,
		Entries:   todayEntries,
	})
	if err != nil {
		return
	}

	today := todayStart.Format("2006-01-02")
	path := filepath.Join(lw.store.dir, "logs-"+today+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return
	}
	// Atomic publish. If rename fails, the next saveLogs tick (or the
	// shutdown flush in main.go) will try again. We stay consistent with
	// the rest of this fire-and-forget save path.
	_ = os.Rename(tmp, path)
}

// loadLogs reads up to logRetentionDays of persisted log entries on startup.
// Uses AddAggregated so existing Count fields from prior sessions are preserved.
func (lw *LogWatcher) loadLogs() {
	if lw.store == nil {
		return
	}
	entries, err := os.ReadDir(lw.store.dir)
	if err != nil {
		return
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -logRetentionDays).Format("2006-01-02")

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "logs-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		date := strings.TrimPrefix(strings.TrimSuffix(name, ".json"), "logs-")
		path := filepath.Join(lw.store.dir, name)

		// Delete files beyond the 7-day retention.
		if date < cutoff {
			os.Remove(path)
			continue
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var f logDayFile
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		if f.MachineID != lw.store.machineID {
			continue
		}
		for _, e := range f.Entries {
			lw.buffer.AddAggregated(e)
		}
	}
}

// DefaultLogFiles auto-detects log files that exist on this system.
// Works across Debian, RHEL, Arch, Alpine, and SUSE.
func DefaultLogFiles() []LogFileConfig {
	return DetectLogFiles()
}

// DefaultLogPatterns returns known error patterns with i18n keys for titles and mitigations.
func DefaultLogPatterns() []LogPattern {
	return []LogPattern{
		// ---- Nginx ----
		{
			Substring:   "worker_connections are not enough",
			Severity:    LogError,
			Source:      "nginx",
			TitleKey:    "logs.nginx_worker_connections_title",
			MitigateKey: "logs.nginx_worker_connections_fix",
		},
		{
			Substring:   "upstream timed out",
			Severity:    LogError,
			Source:      "nginx",
			TitleKey:    "logs.nginx_upstream_timeout_title",
			MitigateKey: "logs.nginx_upstream_timeout_fix",
		},
		{
			Substring:   "Too many open files",
			Severity:    LogError,
			Source:      "nginx",
			TitleKey:    "logs.nginx_too_many_files_title",
			MitigateKey: "logs.nginx_too_many_files_fix",
		},
		{
			Substring:   "SSL_do_handshake() failed",
			Severity:    LogWarn,
			Source:      "nginx",
			TitleKey:    "logs.nginx_ssl_handshake_title",
			MitigateKey: "logs.nginx_ssl_handshake_fix",
		},
		{
			Substring:   "connect() failed (111: Connection refused)",
			Severity:    LogError,
			Source:      "nginx",
			TitleKey:    "logs.nginx_upstream_refused_title",
			MitigateKey: "logs.nginx_upstream_refused_fix",
		},
		{
			Substring:   "client intended to send too large body",
			Severity:    LogWarn,
			Source:      "nginx",
			TitleKey:    "logs.nginx_body_too_large_title",
			MitigateKey: "logs.nginx_body_too_large_fix",
		},
		{
			Substring:   "unlink()",
			Severity:    LogWarn,
			Source:      "nginx",
			TitleKey:    "logs.nginx_cache_unlink_title",
			MitigateKey: "logs.nginx_cache_unlink_fix",
		},

		// ---- Apache ----
		{
			Substring:   "MaxRequestWorkers",
			Severity:    LogError,
			Source:      "apache",
			TitleKey:    "logs.apache_max_workers_title",
			MitigateKey: "logs.apache_max_workers_fix",
		},
		{
			Substring:   "server reached MaxRequestWorkers",
			Severity:    LogError,
			Source:      "apache",
			TitleKey:    "logs.apache_max_workers_title",
			MitigateKey: "logs.apache_max_workers_fix",
		},
		{
			Substring:   "AH00124",
			Severity:    LogError,
			Source:      "apache",
			TitleKey:    "logs.apache_request_timeout_title",
			MitigateKey: "logs.apache_request_timeout_fix",
		},
		{
			Substring:   "No space left on device",
			Severity:    LogError,
			Source:      "apache",
			TitleKey:    "logs.apache_no_space_title",
			MitigateKey: "logs.apache_no_space_fix",
		},
		{
			Substring:   "AH01630",
			Severity:    LogWarn,
			Source:      "apache",
			TitleKey:    "logs.apache_client_denied_title",
			MitigateKey: "logs.apache_client_denied_fix",
		},
		{
			Substring:   "SSL Library Error",
			Severity:    LogError,
			Source:      "apache",
			TitleKey:    "logs.apache_ssl_error_title",
			MitigateKey: "logs.apache_ssl_error_fix",
		},

		// ---- PHP-FPM ----
		{
			Substring:   "Allowed memory size",
			Severity:    LogError,
			Source:      "php-fpm",
			TitleKey:    "logs.php_memory_limit_title",
			MitigateKey: "logs.php_memory_limit_fix",
		},
		{
			Substring:   "Maximum execution time",
			Severity:    LogError,
			Source:      "php-fpm",
			TitleKey:    "logs.php_max_execution_title",
			MitigateKey: "logs.php_max_execution_fix",
		},
		{
			Substring:   "server reached max_children",
			Severity:    LogError,
			Source:      "php-fpm",
			TitleKey:    "logs.php_max_children_title",
			MitigateKey: "logs.php_max_children_fix",
		},
		{
			Substring:   "Call to undefined function",
			Severity:    LogError,
			Source:      "php-fpm",
			TitleKey:    "logs.php_undefined_func_title",
			MitigateKey: "logs.php_undefined_func_fix",
		},
		{
			Substring:   "PHP Fatal error",
			Severity:    LogError,
			Source:      "php-fpm",
			TitleKey:    "logs.php_fatal_title",
			MitigateKey: "logs.php_fatal_fix",
		},
		{
			Substring:   "exited on signal 9",
			Severity:    LogError,
			Source:      "php-fpm",
			TitleKey:    "logs.php_child_killed_title",
			MitigateKey: "logs.php_child_killed_fix",
		},

		// ---- MariaDB ----
		{
			Substring:   "Too many connections",
			Severity:    LogError,
			Source:      "mariadb",
			TitleKey:    "logs.mariadb_too_many_conn_title",
			MitigateKey: "logs.mariadb_too_many_conn_fix",
		},
		{
			Substring:   "Deadlock found",
			Severity:    LogWarn,
			Source:      "mariadb",
			TitleKey:    "logs.mariadb_deadlock_title",
			MitigateKey: "logs.mariadb_deadlock_fix",
		},
		{
			Substring:   "Table is full",
			Severity:    LogError,
			Source:      "mariadb",
			TitleKey:    "logs.mariadb_table_full_title",
			MitigateKey: "logs.mariadb_table_full_fix",
		},
		{
			Substring:   "Got an error reading communication",
			Severity:    LogWarn,
			Source:      "mariadb",
			TitleKey:    "logs.mariadb_comm_error_title",
			MitigateKey: "logs.mariadb_comm_error_fix",
		},
		{
			Substring:   "InnoDB: Unable to lock",
			Severity:    LogError,
			Source:      "mariadb",
			TitleKey:    "logs.mariadb_innodb_lock_title",
			MitigateKey: "logs.mariadb_innodb_lock_fix",
		},

		// ---- System (syslog / kern.log) ----
		{
			Substring:   "Out of memory: Kill process",
			Severity:    LogError,
			Source:      "system",
			TitleKey:    "logs.system_oom_killer_title",
			MitigateKey: "logs.system_oom_killer_fix",
		},
		{
			Substring:   "EXT4-fs error",
			Severity:    LogError,
			Source:      "system",
			TitleKey:    "logs.system_fs_error_title",
			MitigateKey: "logs.system_fs_error_fix",
		},
		{
			Substring:   "segfault at",
			Severity:    LogError,
			Source:      "system",
			TitleKey:    "logs.system_segfault_title",
			MitigateKey: "logs.system_segfault_fix",
		},
		{
			Substring:   "nf_conntrack: table full",
			Severity:    LogError,
			Source:      "system",
			TitleKey:    "logs.system_conntrack_full_title",
			MitigateKey: "logs.system_conntrack_full_fix",
		},
	}
}
