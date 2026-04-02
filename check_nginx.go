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
			Message:   "nginx not running (PID file not found)",
			CheckedAt: time.Now(),
		}
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return CheckResult{
			Component: "nginx-process",
			Status:    StatusCritical,
			Message:   "invalid PID file content",
			CheckedAt: time.Now(),
		}
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return CheckResult{
			Component: "nginx-process",
			Status:    StatusCritical,
			Message:   fmt.Sprintf("process %d not found", pid),
			CheckedAt: time.Now(),
		}
	}

	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return CheckResult{
			Component: "nginx-process",
			Status:    StatusCritical,
			Message:   fmt.Sprintf("process %d not running", pid),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "nginx-process",
		Status:    StatusOK,
		Message:   fmt.Sprintf("running (pid %d)", pid),
		Details:   map[string]string{"pid": strconv.Itoa(pid)},
		CheckedAt: time.Now(),
	}
}

func (c *NginxChecker) checkConfig(ctx context.Context) CheckResult {
	cmd := exec.CommandContext(ctx, "nginx", "-t")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return CheckResult{
			Component: "nginx-config",
			Status:    StatusCritical,
			Message:   "config test failed: " + strings.TrimSpace(string(output)),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "nginx-config",
		Status:    StatusOK,
		Message:   "config valid",
		CheckedAt: time.Now(),
	}
}

func (c *NginxChecker) checkHTTP(ctx context.Context, port int) CheckResult {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: fmt.Sprintf("nginx-http-%d", port),
			Status:    StatusCritical,
			Message:   "request error: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{
			Component: fmt.Sprintf("nginx-http-%d", port),
			Status:    StatusCritical,
			Message:   "connection failed: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return CheckResult{
			Component: fmt.Sprintf("nginx-http-%d", port),
			Status:    StatusCritical,
			Message:   fmt.Sprintf("HTTP %d", resp.StatusCode),
			Details:   map[string]string{"status_code": strconv.Itoa(resp.StatusCode)},
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: fmt.Sprintf("nginx-http-%d", port),
		Status:    StatusOK,
		Message:   fmt.Sprintf("HTTP %d", resp.StatusCode),
		Details:   map[string]string{"status_code": strconv.Itoa(resp.StatusCode)},
		CheckedAt: time.Now(),
	}
}
