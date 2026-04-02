// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPEndpointChecker probes a single HTTP endpoint.
// One checker per configured endpoint; each appears as its own component.
type HTTPEndpointChecker struct {
	cfg    HTTPEndpointConfig
	client *http.Client
	tr     *Translations
}

func NewHTTPEndpointChecker(cfg HTTPEndpointConfig, tr *Translations) *HTTPEndpointChecker {
	timeout := 10 * time.Second
	if cfg.Timeout.Duration > 0 {
		timeout = cfg.Timeout.Duration
	}
	return &HTTPEndpointChecker{
		cfg: cfg,
		tr:  tr,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify},
				MaxIdleConns:    4,
				IdleConnTimeout: 60 * time.Second,
			},
		},
	}
}

// Name returns "http-<name>" so each endpoint appears separately in the dashboard
// and can be referenced independently in healthz.critical_checks.
func (c *HTTPEndpointChecker) Name() string {
	return "http-" + c.cfg.Name
}

func (c *HTTPEndpointChecker) Check(ctx context.Context) []CheckResult {
	method := c.cfg.Method
	if method == "" {
		method = "GET"
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.URL, nil)
	if err != nil {
		return []CheckResult{{
			Component: c.Name(),
			Status:    StatusCritical,
			Message:   c.tr.T("checks.http_request_error", err.Error()),
			CheckedAt: time.Now(),
		}}
	}

	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return []CheckResult{{
			Component: c.Name(),
			Status:    StatusCritical,
			Message:   c.tr.T("checks.http_unreachable", err.Error()),
			CheckedAt: time.Now(),
		}}
	}
	defer resp.Body.Close()

	// Determine expected status codes (default: 200)
	expected := c.cfg.ExpectStatus
	if len(expected) == 0 {
		expected = []int{200}
	}

	statusMatch := false
	for _, s := range expected {
		if resp.StatusCode == s {
			statusMatch = true
			break
		}
	}

	if !statusMatch {
		sev := StatusCritical
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			sev = StatusWarn
		}
		return []CheckResult{{
			Component: c.Name(),
			Status:    sev,
			Message:   c.tr.T("checks.http_unexpected_status", resp.StatusCode),
			CheckedAt: time.Now(),
		}}
	}

	// Optional body assertion
	if c.cfg.ExpectBody != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return []CheckResult{{
				Component: c.Name(),
				Status:    StatusWarn,
				Message:   c.tr.T("checks.http_body_read_error", err.Error()),
				CheckedAt: time.Now(),
			}}
		}
		if !strings.Contains(string(body), c.cfg.ExpectBody) {
			return []CheckResult{{
				Component: c.Name(),
				Status:    StatusWarn,
				Message:   c.tr.T("checks.http_body_not_found", resp.StatusCode),
				CheckedAt: time.Now(),
			}}
		}
	}

	return []CheckResult{{
		Component: c.Name(),
		Status:    StatusOK,
		Message:   c.tr.T("checks.http_ok", resp.StatusCode),
		CheckedAt: time.Now(),
	}}
}
