package main

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
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

// LogWatcher tails multiple log files and matches lines against known patterns.
type LogWatcher struct {
	patterns []LogPattern
	buffer   *RingBuffer
	files    []LogFileConfig
	tr       *Translations
}

// LogFileConfig defines a log file to watch.
type LogFileConfig struct {
	Path   string
	Source string // nginx, php-fpm, mariadb, system
}

func NewLogWatcher(files []LogFileConfig, patterns []LogPattern, bufSize int, tr *Translations) *LogWatcher {
	if bufSize <= 0 {
		bufSize = 200
	}
	return &LogWatcher{
		patterns: patterns,
		buffer:   NewRingBuffer(bufSize),
		files:    files,
		tr:       tr,
	}
}

// Start begins tailing all configured log files. Each file gets its own goroutine.
func (lw *LogWatcher) Start(ctx context.Context) {
	for _, fc := range lw.files {
		go lw.tailFile(ctx, fc)
	}
}

// GetEntries returns all buffered log entries.
func (lw *LogWatcher) GetEntries() []LogEntry {
	return lw.buffer.Entries()
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
			Substring:  "server reached pm.max_children",
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
