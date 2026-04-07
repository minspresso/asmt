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
	Substring  string
	Severity   LogSeverity
	Source     string // which service: nginx, php-fpm, mariadb, system
	TitleKey   string // i18n key for the pattern title
	MitigateKey string // i18n key for the mitigation advice
}

// LogEntry is a single matched log line stored in the ring buffer.
type LogEntry struct {
	Timestamp  time.Time  `json:"timestamp"`
	Source     string     `json:"source"`
	Severity   string     `json:"severity"`
	Line       string     `json:"line"`
	Title      string     `json:"title"`
	Mitigation string     `json:"mitigation"`
}

// RingBuffer is a fixed-size circular buffer for log entries.
// Memory-bounded: stores at most `cap` entries regardless of input volume.
type RingBuffer struct {
	mu    sync.RWMutex
	buf   []LogEntry
	pos   int
	count int
	cap   int
}

func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		buf: make([]LogEntry, capacity),
		cap: capacity,
	}
}

func (rb *RingBuffer) Add(entry LogEntry) {
	rb.mu.Lock()
	rb.buf[rb.pos] = entry
	rb.pos = (rb.pos + 1) % rb.cap
	if rb.count < rb.cap {
		rb.count++
	}
	rb.mu.Unlock()
}

// Entries returns all entries in chronological order (oldest first).
func (rb *RingBuffer) Entries() []LogEntry {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	result := make([]LogEntry, 0, rb.count)
	if rb.count < rb.cap {
		result = append(result, rb.buf[:rb.count]...)
	} else {
		result = append(result, rb.buf[rb.pos:]...)
		result = append(result, rb.buf[:rb.pos]...)
	}
	return result
}

// Count returns the number of entries currently in the buffer.
func (rb *RingBuffer) Count() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.count
}

// Preload inserts historical entries (oldest first) without exceeding capacity.
func (rb *RingBuffer) Preload(entries []LogEntry) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	for _, e := range entries {
		rb.buf[rb.pos] = e
		rb.pos = (rb.pos + 1) % rb.cap
		if rb.count < rb.cap {
			rb.count++
		}
	}
}

// LogWatcher tails multiple log files and matches lines against known patterns.
type LogWatcher struct {
	patterns []LogPattern
	buffer   *RingBuffer
	files    []LogFileConfig
	tr       *Translations
	store    *HistoryStore
	saveN    atomic.Int32           // entries since last persist (accessed from multiple goroutines)
	saveMu   sync.Mutex             // serialises saveLogs calls
	seen     map[string]struct{}    // transient dedup set used only during scanRecent
}

// LogFileConfig defines a log file to watch.
type LogFileConfig struct {
	Path   string
	Source string // nginx, php-fpm, mariadb, system
}

func NewLogWatcher(files []LogFileConfig, patterns []LogPattern, bufSize int, tr *Translations, store *HistoryStore) *LogWatcher {
	if bufSize <= 0 {
		bufSize = 200
	}
	lw := &LogWatcher{
		patterns: patterns,
		buffer:   NewRingBuffer(bufSize),
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
	for _, fc := range lw.files {
		go lw.tailFile(ctx, fc)
	}
}

// scanRecent reads the tail of each log file (up to 64KB) and matches patterns
// against existing lines. This captures incidents that happened before the
// process started. Only entries from the last 24 hours are kept.
// Entries already loaded from disk (via loadLogs) are skipped to avoid duplicates.
func (lw *LogWatcher) scanRecent() {
	// Build a set of existing entries to avoid duplicates from loadLogs.
	existing := lw.buffer.Entries()
	lw.seen = make(map[string]struct{}, len(existing))
	for _, e := range existing {
		lw.seen[e.Timestamp.Format(time.RFC3339)+"|"+e.Line] = struct{}{}
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	for _, fc := range lw.files {
		lw.scanFile(fc, cutoff)
	}
	lw.seen = nil // free memory
	// Persist what we found so it survives the next restart too.
	lw.saveLogs()
}

const scanTailBytes = 64 * 1024 // 64KB per file

func (lw *LogWatcher) scanFile(fc LogFileConfig, cutoff time.Time) {
	f, err := os.Open(fc.Path)
	if err != nil {
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return
	}

	readFrom := int64(0)
	if info.Size() > scanTailBytes {
		readFrom = info.Size() - scanTailBytes
	}
	if _, err := f.Seek(readFrom, io.SeekStart); err != nil {
		return
	}

	buf := make([]byte, info.Size()-readFrom)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return
	}
	buf = buf[:n]

	lines := strings.Split(string(buf), "\n")
	// If we seeked into the middle of the file, drop the first partial line.
	if readFrom > 0 && len(lines) > 0 {
		lines = lines[1:]
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ts := parseLogTimestamp(line)
		if ts.IsZero() || ts.Before(cutoff) {
			continue
		}
		lw.matchLineWithTimestamp(line, fc.Source, ts)
	}
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
	// Syslog: "Apr  6 15:10:23" — Go's "Jan  2" handles both space-padded and zero-padded days.
	if len(line) > 15 {
		if t, err := time.Parse("Jan  2 15:04:05", line[:15]); err == nil {
			t = t.AddDate(time.Now().Year(), 0, 0)
			return t
		}
	}
	return time.Time{}
}

func (lw *LogWatcher) matchLineWithTimestamp(line, source string, ts time.Time) {
	for _, p := range lw.patterns {
		if p.Source != "" && p.Source != source {
			continue
		}
		if strings.Contains(line, p.Substring) {
			truncated := truncate(line, 500)
			// Skip if already loaded from disk (dedup during scanRecent).
			if lw.seen != nil {
				key := ts.Format(time.RFC3339) + "|" + truncated
				if _, dup := lw.seen[key]; dup {
					return
				}
			}
			lw.buffer.Add(LogEntry{
				Timestamp:  ts,
				Source:     source,
				Severity:   p.Severity.String(),
				Line:       truncated,
				Title:      lw.tr.T(p.TitleKey),
				Mitigation: lw.tr.T(p.MitigateKey),
			})
			return
		}
	}
}

// GetEntries returns all buffered log entries.
func (lw *LogWatcher) GetEntries() []LogEntry {
	return lw.buffer.Entries()
}

// GetEntriesSince returns log entries from the last d duration, loading from disk if needed.
func (lw *LogWatcher) GetEntriesSince(d time.Duration) []LogEntry {
	cutoff := time.Now().Add(-d)

	// Start with in-memory entries.
	memEntries := lw.buffer.Entries()

	// Check if memory covers the full range.
	if len(memEntries) > 0 && !memEntries[0].Timestamp.After(cutoff) {
		// Memory buffer covers the range, just filter.
		var result []LogEntry
		for _, e := range memEntries {
			if !e.Timestamp.Before(cutoff) {
				result = append(result, e)
			}
		}
		return result
	}

	// Need disk entries too.
	if lw.store == nil {
		return memEntries
	}

	entries, err := os.ReadDir(lw.store.dir)
	if err != nil {
		return memEntries
	}

	cutoffDate := cutoff.UTC().Format("2006-01-02")
	var all []LogEntry

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "logs-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		date := strings.TrimPrefix(strings.TrimSuffix(name, ".json"), "logs-")
		if date < cutoffDate {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(lw.store.dir, name))
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
			if !e.Timestamp.Before(cutoff) {
				all = append(all, e)
			}
		}
	}

	// Merge with in-memory entries (dedup by timestamp+line).
	seen := make(map[string]struct{})
	for _, e := range all {
		key := e.Timestamp.Format(time.RFC3339Nano) + "|" + e.Line
		seen[key] = struct{}{}
	}
	for _, e := range memEntries {
		key := e.Timestamp.Format(time.RFC3339Nano) + "|" + e.Line
		if _, dup := seen[key]; !dup {
			all = append(all, e)
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})
	return all
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

func (lw *LogWatcher) matchLine(line, source string) {
	for _, p := range lw.patterns {
		if p.Source != "" && p.Source != source {
			continue
		}
		if strings.Contains(line, p.Substring) {
			lw.buffer.Add(LogEntry{
				Timestamp:  time.Now(),
				Source:     source,
				Severity:   p.Severity.String(),
				Line:       truncate(line, 500),
				Title:      lw.tr.T(p.TitleKey),
				Mitigation: lw.tr.T(p.MitigateKey),
			})
			// Persist every 10 new entries (~infrequent writes).
			if lw.saveN.Add(1) >= 10 {
				lw.saveN.Store(0)
				lw.saveLogs()
			}
			return // one match per line is enough
		}
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
func (lw *LogWatcher) saveLogs() {
	if lw.store == nil {
		return
	}
	lw.saveMu.Lock()
	defer lw.saveMu.Unlock()
	if err := os.MkdirAll(lw.store.dir, 0755); err != nil {
		return
	}

	today := time.Now().UTC().Format("2006-01-02")
	todayStart := time.Now().UTC().Truncate(24 * time.Hour)

	entries := lw.buffer.Entries()
	var todayEntries []LogEntry
	for _, e := range entries {
		if !e.Timestamp.Before(todayStart) {
			todayEntries = append(todayEntries, e)
		}
	}
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

	path := filepath.Join(lw.store.dir, "logs-"+today+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, path)
}

// loadLogs reads up to logRetentionDays of persisted log entries on startup.
func (lw *LogWatcher) loadLogs() {
	if lw.store == nil {
		return
	}
	entries, err := os.ReadDir(lw.store.dir)
	if err != nil {
		return
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -logRetentionDays).Format("2006-01-02")
	var all []LogEntry

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "logs-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		date := strings.TrimPrefix(strings.TrimSuffix(name, ".json"), "logs-")
		path := filepath.Join(lw.store.dir, name)

		// Delete old files beyond retention.
		if date <= cutoff {
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
		all = append(all, f.Entries...)
	}

	// Sort oldest-first, then preload into ring buffer.
	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})
	lw.buffer.Preload(all)
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
			Substring:  "worker_connections are not enough",
			Severity:   LogError,
			Source:     "nginx",
			TitleKey:   "logs.nginx_worker_connections_title",
			MitigateKey: "logs.nginx_worker_connections_fix",
		},
		{
			Substring:  "upstream timed out",
			Severity:   LogError,
			Source:     "nginx",
			TitleKey:   "logs.nginx_upstream_timeout_title",
			MitigateKey: "logs.nginx_upstream_timeout_fix",
		},
		{
			Substring:  "Too many open files",
			Severity:   LogError,
			Source:     "nginx",
			TitleKey:   "logs.nginx_too_many_files_title",
			MitigateKey: "logs.nginx_too_many_files_fix",
		},
		{
			Substring:  "SSL_do_handshake() failed",
			Severity:   LogWarn,
			Source:     "nginx",
			TitleKey:   "logs.nginx_ssl_handshake_title",
			MitigateKey: "logs.nginx_ssl_handshake_fix",
		},
		{
			Substring:  "connect() failed (111: Connection refused)",
			Severity:   LogError,
			Source:     "nginx",
			TitleKey:   "logs.nginx_upstream_refused_title",
			MitigateKey: "logs.nginx_upstream_refused_fix",
		},
		{
			Substring:  "client intended to send too large body",
			Severity:   LogWarn,
			Source:     "nginx",
			TitleKey:   "logs.nginx_body_too_large_title",
			MitigateKey: "logs.nginx_body_too_large_fix",
		},
		{
			Substring:  "unlink()",
			Severity:   LogWarn,
			Source:     "nginx",
			TitleKey:   "logs.nginx_cache_unlink_title",
			MitigateKey: "logs.nginx_cache_unlink_fix",
		},

		// ---- Apache ----
		{
			Substring:  "MaxRequestWorkers",
			Severity:   LogError,
			Source:     "apache",
			TitleKey:   "logs.apache_max_workers_title",
			MitigateKey: "logs.apache_max_workers_fix",
		},
		{
			Substring:  "server reached MaxRequestWorkers",
			Severity:   LogError,
			Source:     "apache",
			TitleKey:   "logs.apache_max_workers_title",
			MitigateKey: "logs.apache_max_workers_fix",
		},
		{
			Substring:  "AH00124",
			Severity:   LogError,
			Source:     "apache",
			TitleKey:   "logs.apache_request_timeout_title",
			MitigateKey: "logs.apache_request_timeout_fix",
		},
		{
			Substring:  "No space left on device",
			Severity:   LogError,
			Source:     "apache",
			TitleKey:   "logs.apache_no_space_title",
			MitigateKey: "logs.apache_no_space_fix",
		},
		{
			Substring:  "AH01630",
			Severity:   LogWarn,
			Source:     "apache",
			TitleKey:   "logs.apache_client_denied_title",
			MitigateKey: "logs.apache_client_denied_fix",
		},
		{
			Substring:  "SSL Library Error",
			Severity:   LogError,
			Source:     "apache",
			TitleKey:   "logs.apache_ssl_error_title",
			MitigateKey: "logs.apache_ssl_error_fix",
		},

		// ---- PHP-FPM ----
		{
			Substring:  "Allowed memory size",
			Severity:   LogError,
			Source:     "php-fpm",
			TitleKey:   "logs.php_memory_limit_title",
			MitigateKey: "logs.php_memory_limit_fix",
		},
		{
			Substring:  "Maximum execution time",
			Severity:   LogError,
			Source:     "php-fpm",
			TitleKey:   "logs.php_max_execution_title",
			MitigateKey: "logs.php_max_execution_fix",
		},
		{
			Substring:  "server reached max_children",
			Severity:   LogError,
			Source:     "php-fpm",
			TitleKey:   "logs.php_max_children_title",
			MitigateKey: "logs.php_max_children_fix",
		},
		{
			Substring:  "Call to undefined function",
			Severity:   LogError,
			Source:     "php-fpm",
			TitleKey:   "logs.php_undefined_func_title",
			MitigateKey: "logs.php_undefined_func_fix",
		},
		{
			Substring:  "PHP Fatal error",
			Severity:   LogError,
			Source:     "php-fpm",
			TitleKey:   "logs.php_fatal_title",
			MitigateKey: "logs.php_fatal_fix",
		},
		{
			Substring:  "exited on signal 9",
			Severity:   LogError,
			Source:     "php-fpm",
			TitleKey:   "logs.php_child_killed_title",
			MitigateKey: "logs.php_child_killed_fix",
		},

		// ---- MariaDB ----
		{
			Substring:  "Too many connections",
			Severity:   LogError,
			Source:     "mariadb",
			TitleKey:   "logs.mariadb_too_many_conn_title",
			MitigateKey: "logs.mariadb_too_many_conn_fix",
		},
		{
			Substring:  "Deadlock found",
			Severity:   LogWarn,
			Source:     "mariadb",
			TitleKey:   "logs.mariadb_deadlock_title",
			MitigateKey: "logs.mariadb_deadlock_fix",
		},
		{
			Substring:  "Table is full",
			Severity:   LogError,
			Source:     "mariadb",
			TitleKey:   "logs.mariadb_table_full_title",
			MitigateKey: "logs.mariadb_table_full_fix",
		},
		{
			Substring:  "Got an error reading communication",
			Severity:   LogWarn,
			Source:     "mariadb",
			TitleKey:   "logs.mariadb_comm_error_title",
			MitigateKey: "logs.mariadb_comm_error_fix",
		},
		{
			Substring:  "InnoDB: Unable to lock",
			Severity:   LogError,
			Source:     "mariadb",
			TitleKey:   "logs.mariadb_innodb_lock_title",
			MitigateKey: "logs.mariadb_innodb_lock_fix",
		},

		// ---- System (syslog / kern.log) ----
		{
			Substring:  "Out of memory: Kill process",
			Severity:   LogError,
			Source:     "system",
			TitleKey:   "logs.system_oom_killer_title",
			MitigateKey: "logs.system_oom_killer_fix",
		},
		{
			Substring:  "EXT4-fs error",
			Severity:   LogError,
			Source:     "system",
			TitleKey:   "logs.system_fs_error_title",
			MitigateKey: "logs.system_fs_error_fix",
		},
		{
			Substring:  "segfault at",
			Severity:   LogError,
			Source:     "system",
			TitleKey:   "logs.system_segfault_title",
			MitigateKey: "logs.system_segfault_fix",
		},
		{
			Substring:  "nf_conntrack: table full",
			Severity:   LogError,
			Source:     "system",
			TitleKey:   "logs.system_conntrack_full_title",
			MitigateKey: "logs.system_conntrack_full_fix",
		},
	}
}
