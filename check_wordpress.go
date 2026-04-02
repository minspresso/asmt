package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type WordPressChecker struct {
	URL        string
	ExpectBody string
}

func (c *WordPressChecker) Name() string { return "wordpress" }

func (c *WordPressChecker) Check(ctx context.Context) []CheckResult {
	var results []CheckResult
	results = append(results, c.checkSite(ctx))
	results = append(results, c.checkWPCron(ctx))
	results = append(results, c.checkRESTAPI(ctx))
	return results
}

func (c *WordPressChecker) httpClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func (c *WordPressChecker) checkSite(ctx context.Context) CheckResult {
	client := c.httpClient()
	url := c.URL
	if url == "" {
		url = "http://localhost"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: "wordpress-site",
			Status:    StatusCritical,
			Message:   "request error: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{
			Component: "wordpress-site",
			Status:    StatusCritical,
			Message:   "site unreachable: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return CheckResult{
			Component: "wordpress-site",
			Status:    StatusCritical,
			Message:   fmt.Sprintf("HTTP %d", resp.StatusCode),
			CheckedAt: time.Now(),
		}
	}

	if c.ExpectBody != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return CheckResult{
				Component: "wordpress-site",
				Status:    StatusWarn,
				Message:   fmt.Sprintf("HTTP %d but cannot read body: %s", resp.StatusCode, err.Error()),
				CheckedAt: time.Now(),
			}
		}
		if !strings.Contains(string(body), c.ExpectBody) {
			return CheckResult{
				Component: "wordpress-site",
				Status:    StatusWarn,
				Message:   fmt.Sprintf("HTTP %d but expected content not found", resp.StatusCode),
				CheckedAt: time.Now(),
			}
		}
	}

	return CheckResult{
		Component: "wordpress-site",
		Status:    StatusOK,
		Message:   fmt.Sprintf("HTTP %d - site responding", resp.StatusCode),
		CheckedAt: time.Now(),
	}
}

func (c *WordPressChecker) checkWPCron(ctx context.Context) CheckResult {
	client := c.httpClient()
	url := strings.TrimRight(c.URL, "/") + "/wp-cron.php?doing_wp_cron"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: "wordpress-cron",
			Status:    StatusCritical,
			Message:   "request error: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{
			Component: "wordpress-cron",
			Status:    StatusWarn,
			Message:   "wp-cron unreachable: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return CheckResult{
			Component: "wordpress-cron",
			Status:    StatusCritical,
			Message:   fmt.Sprintf("wp-cron returned HTTP %d", resp.StatusCode),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "wordpress-cron",
		Status:    StatusOK,
		Message:   fmt.Sprintf("wp-cron HTTP %d", resp.StatusCode),
		CheckedAt: time.Now(),
	}
}

func (c *WordPressChecker) checkRESTAPI(ctx context.Context) CheckResult {
	client := c.httpClient()
	url := strings.TrimRight(c.URL, "/") + "/wp-json/"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: "wordpress-api",
			Status:    StatusCritical,
			Message:   "request error: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{
			Component: "wordpress-api",
			Status:    StatusWarn,
			Message:   "REST API unreachable: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return CheckResult{
			Component: "wordpress-api",
			Status:    StatusCritical,
			Message:   fmt.Sprintf("REST API returned HTTP %d", resp.StatusCode),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "wordpress-api",
		Status:    StatusOK,
		Message:   fmt.Sprintf("REST API HTTP %d", resp.StatusCode),
		CheckedAt: time.Now(),
	}
}
