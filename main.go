package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

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

	// Build checkers
	var checkers []Checker
	var mariadbChecker *MariaDBChecker

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

	if cfg.Checks.Nginx.Enabled {
		checkers = append(checkers, NewNginxChecker(cfg.Checks.Nginx.PIDFile, tr))
	}

	if cfg.Checks.PHPFPM.Enabled {
		checkers = append(checkers, &PHPFPMChecker{
			Socket: cfg.Checks.PHPFPM.Socket,
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
			tr,
		))
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

	scheduler := NewScheduler(checkers, cfg.CheckInterval.Duration, alerter, logger, tr)
	go scheduler.Start(ctx)

	// Start HTTP server
	srv := NewServer(scheduler, cfg, logger, tr)
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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	httpServer.Shutdown(shutdownCtx)
}
