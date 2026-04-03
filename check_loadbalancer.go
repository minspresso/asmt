// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type LoadBalancerChecker struct {
	LBIP   string
	tr     *Translations
	client *http.Client
}

func NewLoadBalancerChecker(lbIP string, tr *Translations) *LoadBalancerChecker {
	return &LoadBalancerChecker{
		LBIP: lbIP,
		tr:   tr,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *LoadBalancerChecker) Name() string { return "loadbalancer" }

func (c *LoadBalancerChecker) Check(ctx context.Context) []CheckResult {
	var results []CheckResult
	results = append(results, c.checkMetadata(ctx))
	if c.LBIP != "" {
		results = append(results, c.checkLBPath(ctx))
	}
	return results
}

func (c *LoadBalancerChecker) checkMetadata(ctx context.Context) CheckResult {
	metaClient := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://metadata.google.internal/computeMetadata/v1/instance/zone", nil)
	if err != nil {
		return CheckResult{
			Component: "lb-gcp-metadata",
			Status:    StatusUnknown,
			Message:   c.tr.T("checks.lb_metadata_request_error", err.Error()),
			CheckedAt: time.Now(),
		}
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := metaClient.Do(req)
	if err != nil {
		return CheckResult{
			Component: "lb-gcp-metadata",
			Status:    StatusWarn,
			Message:   c.tr.T("checks.lb_metadata_unreachable", err.Error()),
			CheckedAt: time.Now(),
		}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	zone := strings.TrimSpace(string(body))

	return CheckResult{
		Component: "lb-gcp-metadata",
		Status:    StatusOK,
		Message:   c.tr.T("checks.lb_metadata_ok"),
		Details:   map[string]string{"zone": zone},
		CheckedAt: time.Now(),
	}
}

func (c *LoadBalancerChecker) checkLBPath(ctx context.Context) CheckResult {
	// LBIP is validated in config loading (must be IP or host:port, not a URL)
	url := fmt.Sprintf("http://%s/", c.LBIP)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: "lb-path",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.lb_path_request_error", err.Error()),
			CheckedAt: time.Now(),
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return CheckResult{
			Component: "lb-path",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.lb_path_unreachable", err.Error()),
			CheckedAt: time.Now(),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return CheckResult{
			Component: "lb-path",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.lb_path_error", resp.StatusCode),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "lb-path",
		Status:    StatusOK,
		Message:   c.tr.T("checks.lb_path_ok", resp.StatusCode),
		CheckedAt: time.Now(),
	}
}
