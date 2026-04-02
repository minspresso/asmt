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
			Message:   "cannot read /proc: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Name()[0] < '0' || entry.Name()[0] > '9' {
			continue
		}

		comm, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		if name == "php-fpm" {
			return CheckResult{
				Component: "phpfpm-process",
				Status:    StatusOK,
				Message:   "php-fpm master process running",
				Details:   map[string]string{"pid": entry.Name()},
				CheckedAt: time.Now(),
			}
		}
	}

	return CheckResult{
		Component: "phpfpm-process",
		Status:    StatusCritical,
		Message:   "php-fpm process not found",
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
				Message:   fmt.Sprintf("cannot connect to TCP %s: %s", addr, err.Error()),
				CheckedAt: time.Now(),
			}
		}
		conn.Close()
		return CheckResult{
			Component: "phpfpm-socket",
			Status:    StatusOK,
			Message:   fmt.Sprintf("TCP %s responding", addr),
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
			Message:   fmt.Sprintf("cannot connect to socket %s: %s", socket, err.Error()),
			CheckedAt: time.Now(),
		}
	}
	conn.Close()

	return CheckResult{
		Component: "phpfpm-socket",
		Status:    StatusOK,
		Message:   fmt.Sprintf("socket %s responding", socket),
		CheckedAt: time.Now(),
	}
}
