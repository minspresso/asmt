// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type NginxChecker struct {
	PIDFile string
	tr      *Translations
	client  *http.Client
}

func NewNginxChecker(pidFile string, tr *Translations) *NginxChecker {
	return &NginxChecker{
		PIDFile: pidFile,
		tr:      tr,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *NginxChecker) Name() string { return "nginx" }

func (c *NginxChecker) Check(ctx context.Context) []CheckResult {
	var results []CheckResult
	results = append(results, c.checkProcess())
	results = append(results, c.checkConfig(ctx))
	results = append(results, c.checkHTTP(ctx, 80))
	return results
}

func (c *NginxChecker) checkProcess() CheckResult {
	pidFile := c.PIDFile
	if pidFile == "" {
		pidFile = "/run/nginx.pid"
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		return CheckResult{
			Component: "nginx-process",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.nginx_not_running"),
			CheckedAt: time.Now(),
		}
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return CheckResult{
			Component: "nginx-process",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.nginx_invalid_pid"),
			CheckedAt: time.Now(),
		}
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return CheckResult{
			Component: "nginx-process",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.nginx_process_not_found", pid),
			CheckedAt: time.Now(),
		}
	}

	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return CheckResult{
			Component: "nginx-process",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.nginx_process_not_running", pid),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "nginx-process",
		Status:    StatusOK,
		Message:   c.tr.T("checks.nginx_running", pid),
		Details:   map[string]string{"pid": strconv.Itoa(pid)},
		CheckedAt: time.Now(),
	}
}

func (c *NginxChecker) checkConfig(ctx context.Context) CheckResult {
	cmd := exec.CommandContext(ctx, "nginx", "-t")
	output, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(output))

	if err != nil {
		// nginx -t exits non-zero for reasons unrelated to config syntax.
		// Most commonly it cannot write the PID file or open a log file when
		// running inside a restricted environment (GCP VM, read-only /run,
		// systemd sandbox). If the output explicitly confirms "syntax is ok"
		// AND the only failure is a filesystem/permission error (EROFS, EACCES,
		// ENOENT on /run or /var/log), treat it as fully OK. The config is valid.
		syntaxOK := strings.Contains(out, "syntax is ok")
		fsError := strings.Contains(out, "Read-only file system") ||
			strings.Contains(out, "Permission denied") ||
			strings.Contains(out, "open()") && strings.Contains(out, "failed")
		if syntaxOK && fsError {
			return CheckResult{
				Component: "nginx-config",
				Status:    StatusOK,
				Message:   c.tr.T("checks.nginx_config_valid"),
				CheckedAt: time.Now(),
			}
		}
		return CheckResult{
			Component: "nginx-config",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.nginx_config_failed", out),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "nginx-config",
		Status:    StatusOK,
		Message:   c.tr.T("checks.nginx_config_valid"),
		CheckedAt: time.Now(),
	}
}

func (c *NginxChecker) checkHTTP(ctx context.Context, port int) CheckResult {
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	component := fmt.Sprintf("nginx-http-%d", port)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: component,
			Status:    StatusCritical,
			Message:   c.tr.T("checks.nginx_request_error", err.Error()),
			CheckedAt: time.Now(),
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return CheckResult{
			Component: component,
			Status:    StatusCritical,
			Message:   c.tr.T("checks.nginx_conn_failed", err.Error()),
			CheckedAt: time.Now(),
		}
	}
	defer resp.Body.Close()

	status := StatusOK
	if resp.StatusCode >= 500 {
		status = StatusCritical
	} else if resp.StatusCode >= 400 {
		status = StatusWarn
	}

	return CheckResult{
		Component: component,
		Status:    status,
		Message:   fmt.Sprintf("HTTP %d", resp.StatusCode),
		Details:   map[string]string{"status_code": strconv.Itoa(resp.StatusCode)},
		CheckedAt: time.Now(),
	}
}
