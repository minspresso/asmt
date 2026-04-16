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
				// InsecureSkipVerify is operator-controlled per endpoint and
				// defaults to false. Operators monitoring internal services
				// with self-signed certificates can opt in via the endpoint
				// config. Documented in README "Configuration".
				TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify, MinVersion: tls.VersionTLS12}, //nolint:gosec // G402: opt-in per-endpoint
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
	// Reject non-HTTP URLs. An operator misconfig (or a hostile config
	// writer) otherwise lets us probe file://, unix://, etc.
	if !isHTTPURL(c.cfg.URL) {
		return []CheckResult{{
			Component: c.Name(),
			Status:    StatusCritical,
			Message:   c.tr.T("checks.http_request_error", "url must be http:// or https://"),
			CheckedAt: time.Now(),
		}}
	}
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

	if sev, ok := c.checkStatusCode(resp.StatusCode); !ok {
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

// checkStatusCode returns (severity, false) if the status code does not match
// any expected code, or (_, true) if it matches.
func (c *HTTPEndpointChecker) checkStatusCode(code int) (Status, bool) {
	expected := c.cfg.ExpectStatus
	if len(expected) == 0 {
		expected = []int{200}
	}
	for _, s := range expected {
		if code == s {
			return StatusOK, true
		}
	}
	if code >= 400 && code < 500 {
		return StatusWarn, false
	}
	return StatusCritical, false
}
