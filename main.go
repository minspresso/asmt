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

	// Build checkers
	var checkers []Checker

	if cfg.Checks.LoadBalancer.Enabled {
		checkers = append(checkers, &LoadBalancerChecker{
			LBIP: cfg.Checks.LoadBalancer.LBIP,
		})
	}

	if cfg.Checks.Linux.Enabled {
		checkers = append(checkers, &LinuxChecker{
			DiskWarn:     cfg.Checks.Linux.DiskWarn,
			DiskCritical: cfg.Checks.Linux.DiskCritical,
			MemWarn:      cfg.Checks.Linux.MemWarn,
			MemCritical:  cfg.Checks.Linux.MemCritical,
		})
	}

	if cfg.Checks.Firewall.Enabled {
		checkers = append(checkers, &FirewallChecker{
			Ports: cfg.Checks.Firewall.Ports,
		})
	}

	if cfg.Checks.Nginx.Enabled {
		checkers = append(checkers, &NginxChecker{
			PIDFile: cfg.Checks.Nginx.PIDFile,
		})
	}

	if cfg.Checks.PHPFPM.Enabled {
		checkers = append(checkers, &PHPFPMChecker{
			Socket: cfg.Checks.PHPFPM.Socket,
			Port:   cfg.Checks.PHPFPM.Port,
		})
	}

	if cfg.Checks.MariaDB.Enabled {
		checkers = append(checkers, &MariaDBChecker{
			DSN: cfg.Checks.MariaDB.DSN,
		})
	}

	if cfg.Checks.WordPress.Enabled {
		checkers = append(checkers, &WordPressChecker{
			URL:        cfg.Checks.WordPress.URL,
			ExpectBody: cfg.Checks.WordPress.ExpectBody,
		})
	}

	// Build alerters
	var alerters []Alerter
	if cfg.Alerts.Log.Enabled {
		alerters = append(alerters, &LogAlerter{Logger: logger})
	}
	if cfg.Alerts.Webhook.Enabled && cfg.Alerts.Webhook.URL != "" {
		alerters = append(alerters, NewWebhookAlerter(cfg.Alerts.Webhook.URL))
	}
	if cfg.Alerts.Email.Enabled {
		alerters = append(alerters, &EmailAlerter{
			Host:     cfg.Alerts.Email.SMTPHost,
			Port:     cfg.Alerts.Email.SMTPPort,
			From:     cfg.Alerts.Email.From,
			To:       cfg.Alerts.Email.To,
			Username: cfg.Alerts.Email.Username,
			Password: cfg.Alerts.Email.Password,
		})
	}

	alerter := NewMultiAlerter(alerters...)

	// Start scheduler
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduler := NewScheduler(checkers, cfg.CheckInterval.Duration, alerter, logger)
	go scheduler.Start(ctx)

	// Start HTTP server
	srv := NewServer(scheduler, cfg, logger)
	httpServer := &http.Server{
		Addr:         cfg.Server.Address,
		Handler:      srv.Handler(),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("starting server", "address", cfg.Server.Address)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	fmt.Printf("Server-Stat running on %s\n", cfg.Server.Address)
	fmt.Printf("Dashboard: http://localhost%s\n", cfg.Server.Address)
	fmt.Printf("Health:    http://localhost%s/healthz\n", cfg.Server.Address)
	fmt.Printf("API:       http://localhost%s/api/status\n", cfg.Server.Address)

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("shutting down")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	httpServer.Shutdown(shutdownCtx)
}
