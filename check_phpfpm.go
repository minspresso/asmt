package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type PHPFPMChecker struct {
	Socket string
	Port   int
	tr     *Translations
}

func (c *PHPFPMChecker) Name() string { return "phpfpm" }

func (c *PHPFPMChecker) Check(ctx context.Context) []CheckResult {
	var results []CheckResult
	results = append(results, c.checkProcess())
	results = append(results, c.checkSocket())
	return results
}

func (c *PHPFPMChecker) checkProcess() CheckResult {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return CheckResult{
			Component: "phpfpm-process",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.phpfpm_proc_read_error", err.Error()),
			CheckedAt: time.Now(),
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if len(entry.Name()) == 0 || entry.Name()[0] < '0' || entry.Name()[0] > '9' {
			continue
		}

		comm, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		// Match "php-fpm" and versioned variants like "php-fpm8.2"
		if name == "php-fpm" || strings.HasPrefix(name, "php-fpm") {
			return CheckResult{
				Component: "phpfpm-process",
				Status:    StatusOK,
				Message:   c.tr.T("checks.phpfpm_running"),
				Details:   map[string]string{"pid": entry.Name()},
				CheckedAt: time.Now(),
			}
		}
	}

	return CheckResult{
		Component: "phpfpm-process",
		Status:    StatusCritical,
		Message:   c.tr.T("checks.phpfpm_not_found"),
		CheckedAt: time.Now(),
	}
}

func (c *PHPFPMChecker) checkSocket() CheckResult {
	if c.Port > 0 {
		addr := fmt.Sprintf("127.0.0.1:%d", c.Port)
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			return CheckResult{
				Component: "phpfpm-socket",
				Status:    StatusCritical,
				Message:   c.tr.T("checks.phpfpm_tcp_connect_error", addr, err.Error()),
				CheckedAt: time.Now(),
			}
		}
		conn.Close()
		return CheckResult{
			Component: "phpfpm-socket",
			Status:    StatusOK,
			Message:   c.tr.T("checks.phpfpm_tcp_ok", addr),
			CheckedAt: time.Now(),
		}
	}

	socket := c.Socket
	if socket == "" {
		socket = "/run/php/php-fpm.sock"
	}

	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return CheckResult{
			Component: "phpfpm-socket",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.phpfpm_socket_connect_error", socket, err.Error()),
			CheckedAt: time.Now(),
		}
	}
	conn.Close()

	return CheckResult{
		Component: "phpfpm-socket",
		Status:    StatusOK,
		Message:   c.tr.T("checks.phpfpm_socket_ok", socket),
		CheckedAt: time.Now(),
	}
}
