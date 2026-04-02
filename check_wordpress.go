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
	tr         *Translations
	client     *http.Client
}

func NewWordPressChecker(url, expectBody string, tlsSkipVerify bool, tr *Translations) *WordPressChecker {
	return &WordPressChecker{
		URL:        url,
		ExpectBody: expectBody,
		tr:         tr,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: tlsSkipVerify},
				MaxIdleConns:      4,
				IdleConnTimeout:   60 * time.Second,
				DisableKeepAlives: false,
			},
		},
	}
}

func (c *WordPressChecker) Name() string { return "wordpress" }

func (c *WordPressChecker) Check(ctx context.Context) []CheckResult {
	var results []CheckResult
	results = append(results, c.checkSite(ctx))
	results = append(results, c.checkWPCron(ctx))
	results = append(results, c.checkRESTAPI(ctx))
	return results
}

func (c *WordPressChecker) checkSite(ctx context.Context) CheckResult {
	url := c.URL
	if url == "" {
		url = "http://localhost"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: "wordpress-site",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.wordpress_request_error", err.Error()),
			CheckedAt: time.Now(),
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return CheckResult{
			Component: "wordpress-site",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.wordpress_unreachable", err.Error()),
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

	if resp.StatusCode >= 400 {
		return CheckResult{
			Component: "wordpress-site",
			Status:    StatusWarn,
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
				Message:   c.tr.T("checks.wordpress_body_read_error", resp.StatusCode, err.Error()),
				CheckedAt: time.Now(),
			}
		}
		if !strings.Contains(string(body), c.ExpectBody) {
			return CheckResult{
				Component: "wordpress-site",
				Status:    StatusWarn,
				Message:   c.tr.T("checks.wordpress_body_not_found", resp.StatusCode),
				CheckedAt: time.Now(),
			}
		}
	}

	return CheckResult{
		Component: "wordpress-site",
		Status:    StatusOK,
		Message:   c.tr.T("checks.wordpress_site_ok", resp.StatusCode),
		CheckedAt: time.Now(),
	}
}

func (c *WordPressChecker) checkWPCron(ctx context.Context) CheckResult {
	url := strings.TrimRight(c.URL, "/") + "/wp-cron.php?doing_wp_cron"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: "wordpress-cron",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.wordpress_request_error", err.Error()),
			CheckedAt: time.Now(),
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return CheckResult{
			Component: "wordpress-cron",
			Status:    StatusWarn,
			Message:   c.tr.T("checks.wordpress_cron_unreachable", err.Error()),
			CheckedAt: time.Now(),
		}
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return CheckResult{
			Component: "wordpress-cron",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.wordpress_cron_error", resp.StatusCode),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "wordpress-cron",
		Status:    StatusOK,
		Message:   c.tr.T("checks.wordpress_cron_ok", resp.StatusCode),
		CheckedAt: time.Now(),
	}
}

func (c *WordPressChecker) checkRESTAPI(ctx context.Context) CheckResult {
	url := strings.TrimRight(c.URL, "/") + "/wp-json/"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: "wordpress-api",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.wordpress_request_error", err.Error()),
			CheckedAt: time.Now(),
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return CheckResult{
			Component: "wordpress-api",
			Status:    StatusWarn,
			Message:   c.tr.T("checks.wordpress_api_unreachable", err.Error()),
			CheckedAt: time.Now(),
		}
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return CheckResult{
			Component: "wordpress-api",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.wordpress_api_error", resp.StatusCode),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "wordpress-api",
		Status:    StatusOK,
		Message:   c.tr.T("checks.wordpress_api_ok", resp.StatusCode),
		CheckedAt: time.Now(),
	}
}
