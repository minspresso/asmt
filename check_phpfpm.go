// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

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

// isPHPFPMProcess returns true if `name` is a legitimate php-fpm process
// name. Accepts:
//   - "php-fpm"                        (unversioned)
//   - "php-fpm<digit>..." e.g. "php-fpm8.2", "php-fpm7.4"
//   - "php<digit>.<digit>-fpm"         e.g. "php8.2-fpm" (Debian)
//
// Rejects noise like "php-fpm-monitor", "php-fpmXYZ", "fake-php-fpm".
func isPHPFPMProcess(name string) bool {
	if name == "php-fpm" {
		return true
	}
	// "php-fpm<digit>...": next char after "php-fpm" must be a digit
	if strings.HasPrefix(name, "php-fpm") && len(name) > 7 {
		c := name[7]
		if c >= '0' && c <= '9' {
			return true
		}
	}
	// "phpN.M-fpm": "php" + digit + optional ".digit" + "-fpm"
	if strings.HasPrefix(name, "php") && strings.HasSuffix(name, "-fpm") {
		ver := name[3 : len(name)-4]
		if len(ver) > 0 && ver[0] >= '0' && ver[0] <= '9' {
			return true
		}
	}
	return false
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
		// Match "php-fpm" and versioned variants like "php-fpm8.2",
		// "php-fpm7.4", or "php8.2-fpm" (Debian pattern).
		// Tight match: the name must either equal "php-fpm" exactly OR
		// start with "php-fpm" followed by a version digit/separator, OR
		// match "phpN.M-fpm". This rejects accidental false positives
		// like "php-fpm-monitor" or "php-fpmXYZ".
		if isPHPFPMProcess(name) {
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
