// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// Set a default GOMEMLIMIT only if the operator hasn't already set one
	// via the environment. The Go runtime treats GOMEMLIMIT as a *soft* GC
	// trigger, not an allocation reservation. A generous ceiling costs zero
	// RSS at idle and gives the runtime headroom on the worst day, when
	// buffers fill, the syncer is running, and the tail goroutines are busy.
	// Measured peak on a production VM is ~16 MB; 64 MiB gives ~4× margin.
	// See LEARNINGS.md → "The memory ceiling lesson" for the full story.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(64 * 1024 * 1024)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Load translations
	tr, err := LoadTranslations(cfg.Language)
	if err != nil {
		logger.Error("failed to load translations", "language", cfg.Language, "error", err)
		os.Exit(1)
	}

	// Detect environment
	distro := DetectDistro()
	logger.Info("detected environment", "distro", distro.String())

	// Build checkers
	var checkers []Checker
	var mariadbChecker *MariaDBChecker
	var postgresChecker *PostgreSQLChecker

	if cfg.Checks.LoadBalancer.Enabled {
		checkers = append(checkers, NewLoadBalancerChecker(cfg.Checks.LoadBalancer.LBIP, tr))
	}

	if cfg.Checks.Linux.Enabled {
		checkers = append(checkers, &LinuxChecker{
			DiskWarn:     cfg.Checks.Linux.DiskWarn,
			DiskCritical: cfg.Checks.Linux.DiskCritical,
			MemWarn:      cfg.Checks.Linux.MemWarn,
			MemCritical:  cfg.Checks.Linux.MemCritical,
			tr:           tr,
		})
	}

	if cfg.Checks.Firewall.Enabled {
		checkers = append(checkers, &FirewallChecker{
			Ports: cfg.Checks.Firewall.Ports,
			tr:    tr,
		})
	}

	if cfg.Checks.HTTPServer.Enabled {
		httpType := cfg.Checks.HTTPServer.Type
		if httpType == "" || httpType == "auto" {
			httpType = DetectHTTPServer()
			if httpType != "" {
				logger.Info("auto-detected HTTP server", "type", httpType)
			}
		}
		switch httpType {
		case "nginx":
			pidFile := cfg.Checks.HTTPServer.PIDFile
			if pidFile == "" {
				pidFile = FindNginxPID()
			}
			checkers = append(checkers, NewNginxChecker(pidFile, tr))
		case "apache":
			checkers = append(checkers, NewApacheChecker(cfg.Checks.HTTPServer.PIDFile, tr))
		}
	}

	if cfg.Checks.PHPFPM.Enabled {
		socket := cfg.Checks.PHPFPM.Socket
		if socket == "" && cfg.Checks.PHPFPM.Port == 0 {
			socket = FindPHPFPMSocket()
		}
		checkers = append(checkers, &PHPFPMChecker{
			Socket: socket,
			Port:   cfg.Checks.PHPFPM.Port,
			tr:     tr,
		})
	}

	if cfg.Checks.MariaDB.Enabled && cfg.Checks.MariaDB.DSN != "" {
		mariadbChecker = NewMariaDBChecker(cfg.Checks.MariaDB.DSN, tr)
		checkers = append(checkers, mariadbChecker)
	}

	if cfg.Checks.WordPress.Enabled {
		checkers = append(checkers, NewWordPressChecker(
			cfg.Checks.WordPress.URL,
			cfg.Checks.WordPress.ExpectBody,
			cfg.Checks.WordPress.TLSSkipVerify,
			tr,
		))
	}

	if cfg.Checks.Redis.Enabled {
		checkers = append(checkers, NewRedisChecker(
			cfg.Checks.Redis.Name,
			cfg.Checks.Redis.Addr,
			cfg.Checks.Redis.Password,
			tr,
		))
	}

	if cfg.Checks.PostgreSQL.Enabled && cfg.Checks.PostgreSQL.DSN != "" {
		postgresChecker = NewPostgreSQLChecker(
			cfg.Checks.PostgreSQL.Name,
			cfg.Checks.PostgreSQL.DSN,
			tr,
		)
		checkers = append(checkers, postgresChecker)
	}

	for _, ep := range cfg.Checks.HTTPEndpoints {
		if ep.Enabled && ep.URL != "" && ep.Name != "" {
			checkers = append(checkers, NewHTTPEndpointChecker(ep, tr))
		}
	}

	if cfg.Checks.SSLCertificates.Enabled {
		domains := sslDomains(cfg)
		if len(domains) > 0 {
			checkers = append(checkers, NewSSLChecker(
				domains,
				cfg.Checks.SSLCertificates.WarnDays,
				cfg.Checks.SSLCertificates.CriticalDays,
				tr,
			))
		}
	}

	// Build alerters
	var alerters []Alerter
	if cfg.Alerts.Log.Enabled {
		alerters = append(alerters, &LogAlerter{Logger: logger, tr: tr})
	}
	if cfg.Alerts.Webhook.Enabled && cfg.Alerts.Webhook.URL != "" {
		alerters = append(alerters, NewWebhookAlerter(cfg.Alerts.Webhook.URL, tr))
	}
	if cfg.Alerts.Email.Enabled {
		alerters = append(alerters, &EmailAlerter{
			Host:     cfg.Alerts.Email.SMTPHost,
			Port:     cfg.Alerts.Email.SMTPPort,
			From:     cfg.Alerts.Email.From,
			To:       cfg.Alerts.Email.To,
			Username: cfg.Alerts.Email.Username,
			Password: cfg.Alerts.Email.Password,
			tr:       tr,
		})
	}

	alerter := NewMultiAlerter(alerters...)

	// Start scheduler
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	historyStore := NewHistoryStore(*configPath)
	scheduler := NewScheduler(checkers, cfg.CheckInterval.Duration, alerter, logger, tr, historyStore)

	// Start log watcher
	var logWatcher *LogWatcher
	if cfg.Logs.Enabled {
		var logFiles []LogFileConfig
		if len(cfg.Logs.Files) > 0 {
			for _, path := range cfg.Logs.Files {
				source := guessLogSource(path)
				logFiles = append(logFiles, LogFileConfig{Path: path, Source: source})
			}
		} else {
			logFiles = DefaultLogFiles()
		}
		logWatcher = NewLogWatcher(logFiles, DefaultLogPatterns(), cfg.Logs.BufferSize, tr, historyStore)
		// Wire log watcher into scheduler so check results (warn/critical)
		// are recorded into the unified log timeline. Note: our check events
		// are observations, not authoritative records. The OS / software logs
		// (journalctl, syslog, nginx error.log, etc.) are the source of truth.
		// When users need to verify or investigate, the dashboard points them
		// there rather than relying on our own potentially-lossy buffer.
		scheduler.SetLogWatcher(logWatcher)
		go logWatcher.Start(ctx)
		logger.Info("log watcher started", "files", len(logFiles), "buffer_size", cfg.Logs.BufferSize)
	}

	// Start scheduler AFTER wiring logWatcher so the very first check cycle
	// can record any warn/critical results into the log buffer.
	go scheduler.Start(ctx)

	// Build a Syncer that pulls from the systemd journal and feeds our
	// buffer. If journalctl isn't installed (e.g., Alpine), Syncer is
	// disabled and the API handler returns 501.
	var syncer *Syncer
	if logWatcher != nil {
		syncer = NewSyncer(logWatcher.buffer, logger)
		if syncer.Enabled() {
			logger.Info("syncer ready (journalctl available)")
			// Initial sync on startup: catches up any history the real-time
			// tail goroutines missed while the process was down. Runs in a
			// goroutine so HTTP server can start immediately.
			go func() {
				syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				res, err := syncer.Sync(syncCtx)
				if err != nil {
					logger.Warn("initial sync failed", "error", err)
					return
				}
				logger.Info("initial sync complete",
					"chunks", res.ChunksRun,
					"lines_parsed", res.LinesParsed,
					"events_added", res.EventsAdded,
					"buffer_after", res.BufferAfter,
					"duration", res.CompletedAt.Sub(res.StartedAt).String(),
				)
			}()
			// Background auto-sync: every hour, catch up anything new.
			go func() {
				ticker := time.NewTicker(1 * time.Hour)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
						res, err := syncer.Sync(syncCtx)
						cancel()
						if err != nil {
							logger.Warn("auto-sync failed", "error", err)
							continue
						}
						logger.Debug("auto-sync complete",
							"events_added", res.EventsAdded,
							"buffer_after", res.BufferAfter)
					}
				}
			}()
		} else {
			logger.Info("syncer disabled (journalctl not available)")
		}
	}

	// Loud warning if the tool is configured to listen on a non-loopback
	// address. ASMT has NO built-in authentication; operators who bind
	// outside 127.0.0.1 must put a reverse proxy with auth in front of it.
	// We can't refuse to start (some operators intentionally expose via a
	// trusted private network), but we make absolutely sure the operator
	// knows what they're doing.
	if !isLoopbackBind(cfg.Server.Address) {
		logger.Warn("LISTENING ON NON-LOOPBACK ADDRESS WITHOUT BUILT-IN AUTHENTICATION",
			"address", cfg.Server.Address,
			"risk", "any client on the network can reach the dashboard and trigger syncs",
			"mitigation", "put an authenticating reverse proxy in front, or rebind to 127.0.0.1:8080",
		)
	}

	// Start HTTP server
	srv := NewServer(scheduler, logWatcher, syncer, cfg, logger, tr)
	httpServer := &http.Server{
		Addr:    cfg.Server.Address,
		Handler: srv.Handler(),
		// ReadHeaderTimeout defends against Slowloris header attacks: even if
		// the client trickles headers one byte at a time, they must complete
		// the header block within this window or be disconnected.
		ReadHeaderTimeout: 5 * time.Second,
		// ReadTimeout covers the total read (headers + body).
		ReadTimeout: 10 * time.Second,
		// WriteTimeout must exceed syncSubprocessTimeout (15s) so POST /api/sync
		// can finish running journalctl and still deliver its response.
		// Normal endpoints complete in milliseconds.
		WriteTimeout: 25 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.Info(tr.T("server.starting"), "address", cfg.Server.Address)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(tr.T("server.server_failed"), "error", err)
			os.Exit(1)
		}
	}()

	fmt.Printf(tr.T("server.running", cfg.Server.Address) + "\n")
	fmt.Printf(tr.T("server.dashboard_url", cfg.Server.Address) + "\n")
	fmt.Printf(tr.T("server.health_url", cfg.Server.Address) + "\n")
	fmt.Printf(tr.T("server.api_url", cfg.Server.Address) + "\n")

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info(tr.T("server.shutting_down"))
	cancel()

	// Flush log entries to disk before exiting.
	if logWatcher != nil {
		logWatcher.saveLogs()
	}

	// Clean up persistent connections
	if mariadbChecker != nil {
		mariadbChecker.Close()
	}
	if postgresChecker != nil {
		postgresChecker.Close()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	httpServer.Shutdown(shutdownCtx)
}

// sslDomains builds a deduplicated list of HTTPS hosts to check.
// Starts from explicit config, then adds hosts from any https:// WordPress
// or HTTP endpoint URLs already configured. No separate config entry needed.
func sslDomains(cfg *Config) []string {
	seen := make(map[string]struct{})
	var out []string

	add := func(host string) {
		if host == "" {
			return
		}
		if _, dup := seen[host]; !dup {
			seen[host] = struct{}{}
			out = append(out, host)
		}
	}

	for _, d := range cfg.Checks.SSLCertificates.Domains {
		add(strings.TrimSpace(d))
	}

	if cfg.Checks.WordPress.Enabled && strings.HasPrefix(cfg.Checks.WordPress.URL, "https://") {
		add(strings.TrimPrefix(strings.SplitN(cfg.Checks.WordPress.URL[8:], "/", 2)[0], "www."))
	}

	for _, ep := range cfg.Checks.HTTPEndpoints {
		if ep.Enabled && strings.HasPrefix(ep.URL, "https://") {
			add(strings.SplitN(ep.URL[8:], "/", 2)[0])
		}
	}

	// Auto-detect domains from nginx server blocks listening on 443/ssl.
	for _, d := range nginxDomains() {
		add(d)
	}

	return out
}

// isLoopbackBind returns true if the configured address is a loopback
// interface. Accepts: "127.0.0.1:8080", "localhost:8080", "[::1]:8080",
// and any host:port whose host resolves to a loopback literal.
// Returns false for "0.0.0.0", empty host (all interfaces), or external IPs.
func isLoopbackBind(addr string) bool {
	// Split host:port; if parsing fails, treat as non-loopback (safe default).
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// guessLogSource infers the service name from a log file path.
func guessLogSource(path string) string {
	switch {
	case strings.Contains(path, "nginx"):
		return "nginx"
	case strings.Contains(path, "php") && strings.Contains(path, "fpm"):
		return "php-fpm"
	case strings.Contains(path, "mysql") || strings.Contains(path, "mariadb"):
		return "mariadb"
	default:
		return "system"
	}
}
