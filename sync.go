// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso
//
// sync.go: authoritative log synchronization.
//
// DESIGN
// ======
// Our own in-memory buffer is a cache. The OS and software keep durable,
// timestamped logs of every significant event. This module periodically
// (and on user demand) pulls from those authoritative sources and feeds
// them into our aggregated buffer, so the dashboard is always showing
// reality rather than just "what we happened to see".
//
// Sources:
//  1. systemd journal (journalctl): unified system events, priority-filtered
//  2. kernel log (journalctl -k): OOM-killer, disk/hardware/nic errors
//
// We do NOT re-scan the tailed log files during sync: the tail goroutines
// already capture those in real time, and scanning gigabytes of nginx logs
// every sync would be wasteful. Startup scanRecent (in logwatch.go) still
// runs a bounded tail scan on process start to cover recent history.
//
// LOAD HANDLING
// =============
// Design target: sustain 500 matched events/second for 24+ hours without
// degradation. The architecture makes this trivial at steady state because
// aggregation collapses all events for the same (15-min bucket, title,
// source) into one LogEntry with a count. 500/sec × 900s = 450,000 events
// per 15-min bucket, but only ONE buffer entry per unique error type.
//
// The hard case is the parse burst when Sync() catches up on a window that
// already contains millions of events. This module handles it by:
//   - Streaming subprocess stdout line-by-line (bufio.Scanner).
//   - Parsing one line, calling buffer.AddEvent, discarding immediately.
//   - Never accumulating raw entries in a slice: memory during parse is
//     O(unique aggregation keys), not O(raw events).
//   - Chunking large windows into 24h-or-less pieces to bound runtime
//     per subprocess call and let the GC release between chunks.
//   - Per-subprocess: 16 MB stdout cap, 15s timeout, 50k line cap.
//
// SAFETY
// ======
//   - Strict argument validation: user input never reaches exec.Command
//     directly. Dates are parsed into time.Time, then formatted into
//     @<unix-seconds> form we control.
//   - Bounded resources per subprocess call.
//   - A single-flight mutex prevents concurrent syncs from stampeding the
//     process list and blowing memory limits.
//   - Graceful degradation: if journalctl isn't available, Syncer is
//     disabled and the caller gets ErrJournalctlUnavailable.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors surfaced to callers.
var (
	ErrJournalctlUnavailable = errors.New("journalctl not available on this system")
	ErrSyncInProgress        = errors.New("sync already in progress")
	ErrInvalidDate           = errors.New("date must be in YYYY-MM-DD format")
)

// Sync tunables.
const (
	// Per-subprocess hard timeout.
	syncSubprocessTimeout = 15 * time.Second

	// Per-subprocess stdout byte cap. Protects against pathological
	// journalctl output from a server generating millions of events/day.
	syncMaxBytes = 16 * 1024 * 1024 // 16MB

	// Per-subprocess line cap. With aggregation, 50k entries per chunk
	// is more than enough to populate the dashboard even for a server
	// under sustained 500 events/second load.
	syncMaxLines = 50000

	// Windowing: we pull history in 24-hour slices so each subprocess
	// call is bounded. Larger windows are chunked.
	syncChunkHours = 24

	// On startup (or when lastSync is unknown), sync this far back.
	syncDefaultWindow = 7 * 24 * time.Hour
)

// SyncResult is returned from every Sync call for display/logging.
//
// Error handling note: the Errors slice contains only high-level labels
// ("chunk 1 failed"), never raw subprocess output or paths. Full error
// details are logged server-side via the application logger. This prevents
// leaking internal paths, subprocess output, or exec details through the
// HTTP API.
type SyncResult struct {
	StartedAt    time.Time `json:"started_at"`
	CompletedAt  time.Time `json:"completed_at"`
	FromTime     time.Time `json:"from"`
	ToTime       time.Time `json:"to"`
	ChunksRun    int       `json:"chunks"`
	ChunksFailed int       `json:"chunks_failed,omitempty"`
	LinesParsed  int       `json:"lines_parsed"`
	EventsAdded  int       `json:"events_added"` // events fed into AddEvent
	BufferAfter  int       `json:"buffer_after"` // buffer len after sync
	SubprocCalls int       `json:"subproc_calls"`
	Errors       []string  `json:"errors,omitempty"` // safe high-level labels only
}

// Syncer pulls from authoritative log sources (systemd journal) and
// stream-aggregates the results into the main log buffer.
type Syncer struct {
	buffer  *logBuffer
	enabled bool // false if journalctl is unavailable
	logger  *slog.Logger

	// single-flight guard: only one Sync runs at a time. Concurrent
	// callers get ErrSyncInProgress, which keeps memory bounded under
	// an aggressive UI refresh loop.
	runMu sync.Mutex

	// Protected by stateMu.
	stateMu  sync.RWMutex
	lastSync time.Time
	lastRes  *SyncResult
	inFlight bool
}

// NewSyncer checks for journalctl and returns a ready Syncer.
// Safe for concurrent use. The logger is used for detailed internal error
// logging; HTTP responses never include raw error text.
func NewSyncer(buf *logBuffer, logger *slog.Logger) *Syncer {
	_, err := exec.LookPath("journalctl")
	if logger == nil {
		logger = slog.Default()
	}
	return &Syncer{
		buffer:  buf,
		enabled: err == nil,
		logger:  logger,
	}
}

// Enabled reports whether journalctl is available on this host.
func (s *Syncer) Enabled() bool { return s.enabled }

// LastSync returns when the most recent successful sync finished.
// Zero time means never synced.
func (s *Syncer) LastSync() time.Time {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.lastSync
}

// LastResult returns the most recent sync result (nil if never run).
func (s *Syncer) LastResult() *SyncResult {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.lastRes
}

// InFlight reports whether a sync is currently running.
func (s *Syncer) InFlight() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.inFlight
}

// Sync pulls authoritative log events from the window starting at
// (lastSync or now-syncDefaultWindow) through now, chunked into
// syncChunkHours pieces. Stream-aggregates into the buffer.
// Only one Sync runs at a time (single-flight via runMu).
func (s *Syncer) Sync(ctx context.Context) (*SyncResult, error) {
	if !s.enabled {
		return nil, ErrJournalctlUnavailable
	}

	// Single-flight. TryLock avoids building up goroutines behind a
	// long-running sync when the UI is hitting refresh.
	if !s.runMu.TryLock() {
		return nil, ErrSyncInProgress
	}
	defer s.runMu.Unlock()

	s.stateMu.Lock()
	s.inFlight = true
	s.stateMu.Unlock()
	defer func() {
		s.stateMu.Lock()
		s.inFlight = false
		s.stateMu.Unlock()
	}()

	now := time.Now().UTC()
	from := s.LastSync().UTC()
	if from.IsZero() || now.Sub(from) > syncDefaultWindow {
		from = now.Add(-syncDefaultWindow)
	}

	res := &SyncResult{
		StartedAt: time.Now().UTC(),
		FromTime:  from,
		ToTime:    now,
	}

	// Chunk the window into syncChunkHours slices so each subprocess
	// call stays bounded. For a 7-day catch-up, that's 7 chunks.
	chunkDur := syncChunkHours * time.Hour
	for chunkStart := from; chunkStart.Before(now); chunkStart = chunkStart.Add(chunkDur) {
		chunkEnd := chunkStart.Add(chunkDur)
		if chunkEnd.After(now) {
			chunkEnd = now
		}
		res.ChunksRun++
		if err := s.syncChunk(ctx, chunkStart, chunkEnd, res); err != nil {
			// Log full error detail server-side; surface only a safe
			// label via the HTTP response to avoid leaking paths,
			// subprocess output, or exec details.
			res.ChunksFailed++
			res.Errors = append(res.Errors,
				fmt.Sprintf("chunk %d failed", res.ChunksRun))
			s.logger.Warn("sync chunk failed",
				"chunk_index", res.ChunksRun,
				"chunk_start", chunkStart.Format(time.RFC3339),
				"error", err)
			// Don't abort. Continue with remaining chunks so partial
			// data is better than nothing.
		}
		if ctx.Err() != nil {
			break
		}
	}

	res.CompletedAt = time.Now().UTC()
	res.BufferAfter = s.buffer.Len()

	// Stream parsing allocates a lot of transient strings. Release
	// back to the OS before the next sync cycle.
	runtime.GC()
	debug.FreeOSMemory()

	s.stateMu.Lock()
	s.lastSync = res.CompletedAt
	s.lastRes = res
	s.stateMu.Unlock()
	return res, nil
}

// syncChunk runs userspace and kernel journalctl queries for a single
// time window SEQUENTIALLY, stream-aggregating each parsed line into the
// buffer. Sequential (not parallel) to keep peak memory bounded: only one
// json decode stream is active at a time. Between the two calls, we GC
// to release the transient strings from the first call.
// Errors are returned but don't halt the overall sync.
func (s *Syncer) syncChunk(ctx context.Context, from, to time.Time, res *SyncResult) error {
	sinceStr := fmt.Sprintf("@%d", from.Unix())
	untilStr := fmt.Sprintf("@%d", to.Unix())
	maxLines := strconv.Itoa(syncMaxLines)

	// Userspace events: warning or worse. Priority=warning filters on the
	// journalctl side so we don't stream INFO/DEBUG noise.
	lines, added, err := s.streamJournalctl(ctx,
		"--since", sinceStr,
		"--until", untilStr,
		"--priority=warning",
		"--output=json",
		"--no-pager",
		"-n", maxLines,
	)
	res.SubprocCalls++
	res.LinesParsed += lines
	res.EventsAdded += added
	if err != nil {
		return fmt.Errorf("userspace: %w", err)
	}

	// Release parse-transient memory before the second subprocess.
	runtime.GC()

	// Kernel events: no priority filter (kernel messages aren't always
	// priority-tagged the way userspace ones are, and we want OOM-killer,
	// disk errors, segfaults at any level).
	lines, added, err = s.streamJournalctl(ctx,
		"-k",
		"--since", sinceStr,
		"--until", untilStr,
		"--output=json",
		"--no-pager",
		"-n", maxLines,
	)
	res.SubprocCalls++
	res.LinesParsed += lines
	res.EventsAdded += added
	if err != nil {
		return fmt.Errorf("kernel: %w", err)
	}
	return nil
}

// streamJournalctl runs journalctl with the given args, parses each
// output line, feeds matching entries directly into the buffer, and
// never accumulates a slice of raw entries. Memory during parse is
// O(bucket keys), not O(lines).
//
// Returns (lines_parsed, entries_added, error).
func (s *Syncer) streamJournalctl(ctx context.Context, args ...string) (int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, syncSubprocessTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "journalctl", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, 0, err
	}

	// Hard byte cap + line cap. The bufio.Scanner reads line-by-line
	// so working set stays tiny regardless of total output size.
	limited := io.LimitReader(stdout, syncMaxBytes)
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	lines, added := 0, 0
	for scanner.Scan() {
		lines++
		if entry, ok := parseJournalLine(scanner.Bytes()); ok {
			s.buffer.AddEvent(entry) // stream-aggregate; no accumulation
			added++
		}
	}
	scanErr := scanner.Err()
	// Drain+wait always to avoid zombie. If ctx timed out, Wait returns
	// the kill signal error. We report it only if scanning didn't get
	// the real underlying error first.
	waitErr := cmd.Wait()

	if scanErr != nil {
		if scanErr == bufio.ErrTooLong {
			// A single journal entry exceeded the 1MB scanner max.
			// This is not a fatal error (the rest of the scan already
			// completed), but it DOES mean we silently lost an entry
			// that might have been important. Log it server-side so
			// operators can investigate if they see the warning.
			s.logger.Warn("journal entry exceeded 1MB scanner buffer; entry skipped",
				"lines_parsed", lines,
				"events_added", added)
		} else {
			return lines, added, scanErr
		}
	}
	if waitErr != nil && ctx.Err() == nil {
		return lines, added, waitErr
	}
	return lines, added, nil
}

// journalFields holds only the keys we care about from journalctl JSON
// output. Journal entries have dozens of fields; we decode the rest
// into an implicit discard to save CPU and allocation.
type journalFields struct {
	RealtimeTimestamp string `json:"__REALTIME_TIMESTAMP"` // microseconds since epoch
	Priority          string `json:"PRIORITY"`             // "0"-"7"
	Identifier        string `json:"SYSLOG_IDENTIFIER"`
	Unit              string `json:"_SYSTEMD_UNIT"`
	Comm              string `json:"_COMM"`
	Message           string `json:"MESSAGE"`
}

// parseJournalLine decodes one line of journalctl -o json output into a
// LogEntry. Returns (entry, false) for lines that can't be parsed or
// aren't interesting (info/notice/debug priority).
func parseJournalLine(line []byte) (LogEntry, bool) {
	if len(line) == 0 {
		return LogEntry{}, false
	}
	var f journalFields
	if err := json.Unmarshal(line, &f); err != nil {
		return LogEntry{}, false
	}
	if f.Message == "" {
		return LogEntry{}, false
	}

	// Microseconds since epoch → time.Time.
	//
	// Defensive bounds: real journal timestamps are always non-negative
	// and fit comfortably in int64 microseconds. A malicious or corrupted
	// journalctl could emit values that cause overflow when multiplied
	// by 1000 (nanoseconds per microsecond):
	//   max safe usec = math.MaxInt64 / 1000 ≈ 9.2e15 microseconds
	//                 ≈ year 294247
	// Anything outside [0, 9.2e15] is rejected to prevent silently-wrong
	// timestamps and year-2262 overflow bugs.
	usec, err := strconv.ParseInt(f.RealtimeTimestamp, 10, 64)
	if err != nil {
		return LogEntry{}, false
	}
	const maxSafeUsec int64 = 9223372036854775 // math.MaxInt64 / 1000
	if usec < 0 || usec > maxSafeUsec {
		return LogEntry{}, false
	}
	ts := time.Unix(0, usec*int64(time.Microsecond)).UTC()

	// Map syslog priority to our severity levels.
	// 0=emerg, 1=alert, 2=crit, 3=err  → "error"
	// 4=warning                        → "warn"
	// 5+ (notice, info, debug)         → skip
	// Kernel messages without a PRIORITY field are treated as warn
	// (journalctl -k only returns kernel messages so they're relevant).
	severity := ""
	switch f.Priority {
	case "0", "1", "2", "3":
		severity = "error"
	case "4":
		severity = "warn"
	case "":
		severity = "warn"
	default:
		return LogEntry{}, false
	}

	// Title preference: syslog identifier → systemd unit → _COMM → "system".
	title := f.Identifier
	if title == "" {
		title = strings.TrimSuffix(f.Unit, ".service")
	}
	if title == "" {
		title = f.Comm
	}
	if title == "" {
		title = "system"
	}

	// Sample message (capped). The full entry is always in the journal.
	// Our buffer is a summary, not an archive.
	sample := f.Message
	if len(sample) > 500 {
		sample = sample[:500] + "..."
	}

	return LogEntry{
		Timestamp: ts,
		Source:    "journal",
		Severity:  severity,
		Line:      sample,
		Title:     title,
	}, true
}
