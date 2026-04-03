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

type ApacheChecker struct {
	PIDFile string
	tr      *Translations
	client  *http.Client
}

func NewApacheChecker(pidFile string, tr *Translations) *ApacheChecker {
	if pidFile == "" {
		pidFile = FindApachePID()
	}
	return &ApacheChecker{
		PIDFile: pidFile,
		tr:      tr,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *ApacheChecker) Name() string { return "apache" }

func (c *ApacheChecker) Check(ctx context.Context) []CheckResult {
	var results []CheckResult
	results = append(results, c.checkProcess())
	results = append(results, c.checkConfig(ctx))
	results = append(results, c.checkHTTP(ctx, 80))
	return results
}

func (c *ApacheChecker) checkProcess() CheckResult {
	if c.PIDFile == "" {
		return CheckResult{
			Component: "apache-process",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.apache_not_running"),
			CheckedAt: time.Now(),
		}
	}

	data, err := os.ReadFile(c.PIDFile)
	if err != nil {
		return CheckResult{
			Component: "apache-process",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.apache_not_running"),
			CheckedAt: time.Now(),
		}
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return CheckResult{
			Component: "apache-process",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.apache_invalid_pid"),
			CheckedAt: time.Now(),
		}
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return CheckResult{
			Component: "apache-process",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.apache_process_not_found", pid),
			CheckedAt: time.Now(),
		}
	}

	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return CheckResult{
			Component: "apache-process",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.apache_process_not_running", pid),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "apache-process",
		Status:    StatusOK,
		Message:   c.tr.T("checks.apache_running", pid),
		Details:   map[string]string{"pid": strconv.Itoa(pid)},
		CheckedAt: time.Now(),
	}
}

func (c *ApacheChecker) checkConfig(ctx context.Context) CheckResult {
	cmdName, cmdArgs := ApacheConfigTestCmd()
	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return CheckResult{
			Component: "apache-config",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.apache_config_failed", strings.TrimSpace(string(output))),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "apache-config",
		Status:    StatusOK,
		Message:   c.tr.T("checks.apache_config_valid"),
		CheckedAt: time.Now(),
	}
}

func (c *ApacheChecker) checkHTTP(ctx context.Context, port int) CheckResult {
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	component := fmt.Sprintf("apache-http-%d", port)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: component,
			Status:    StatusCritical,
			Message:   c.tr.T("checks.apache_request_error", err.Error()),
			CheckedAt: time.Now(),
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return CheckResult{
			Component: component,
			Status:    StatusCritical,
			Message:   c.tr.T("checks.apache_conn_failed", err.Error()),
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
