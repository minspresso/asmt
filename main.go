// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
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

	// Cap Go runtime memory to 50MB.
	// This controls GC aggressiveness - Go will GC more often to stay under this limit.
	debug.SetMemoryLimit(50 * 1024 * 1024)

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
	go scheduler.Start(ctx)

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
		logWatcher = NewLogWatcher(logFiles, DefaultLogPatterns(), cfg.Logs.BufferSize, tr)
		go logWatcher.Start(ctx)
		logger.Info("log watcher started", "files", len(logFiles), "buffer_size", cfg.Logs.BufferSize)
	}

	// Start HTTP server
	srv := NewServer(scheduler, logWatcher, cfg, logger, tr)
	httpServer := &http.Server{
		Addr:         cfg.Server.Address,
		Handler:      srv.Handler(),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
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
