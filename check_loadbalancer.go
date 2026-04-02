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
	LBIP string
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
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://metadata.google.internal/computeMetadata/v1/instance/zone", nil)
	if err != nil {
		return CheckResult{
			Component: "lb-gcp-metadata",
			Status:    StatusUnknown,
			Message:   "cannot create metadata request: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{
			Component: "lb-gcp-metadata",
			Status:    StatusWarn,
			Message:   "GCP metadata server unreachable (may not be on GCP): " + err.Error(),
			CheckedAt: time.Now(),
		}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	zone := strings.TrimSpace(string(body))

	return CheckResult{
		Component: "lb-gcp-metadata",
		Status:    StatusOK,
		Message:   "GCP instance detected",
		Details:   map[string]string{"zone": zone},
		CheckedAt: time.Now(),
	}
}

func (c *LoadBalancerChecker) checkLBPath(ctx context.Context) CheckResult {
	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("http://%s/", c.LBIP)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return CheckResult{
			Component: "lb-path",
			Status:    StatusCritical,
			Message:   "request error: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{
			Component: "lb-path",
			Status:    StatusCritical,
			Message:   "LB unreachable: " + err.Error(),
			CheckedAt: time.Now(),
		}
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return CheckResult{
			Component: "lb-path",
			Status:    StatusCritical,
			Message:   fmt.Sprintf("LB returned HTTP %d", resp.StatusCode),
			CheckedAt: time.Now(),
		}
	}

	return CheckResult{
		Component: "lb-path",
		Status:    StatusOK,
		Message:   fmt.Sprintf("LB path OK (HTTP %d)", resp.StatusCode),
		CheckedAt: time.Now(),
	}
}
