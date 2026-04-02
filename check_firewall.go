package main

import (
	"context"
	"fmt"
	"net"
	"time"
)

type FirewallChecker struct {
	Ports []int
	tr    *Translations
}

func (c *FirewallChecker) Name() string { return "firewall" }

func (c *FirewallChecker) Check(ctx context.Context) []CheckResult {
	var results []CheckResult
	for _, port := range c.Ports {
		results = append(results, c.checkPort(port))
	}
	return results
}

func (c *FirewallChecker) checkPort(port int) CheckResult {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return CheckResult{
			Component: fmt.Sprintf("firewall-port-%d", port),
			Status:    StatusCritical,
			Message:   c.tr.T("checks.firewall_port_unreachable", port, err.Error()),
			Details:   map[string]string{"port": fmt.Sprintf("%d", port)},
			CheckedAt: time.Now(),
		}
	}
	conn.Close()

	return CheckResult{
		Component: fmt.Sprintf("firewall-port-%d", port),
		Status:    StatusOK,
		Message:   c.tr.T("checks.firewall_port_open", port),
		Details:   map[string]string{"port": fmt.Sprintf("%d", port)},
		CheckedAt: time.Now(),
	}
}
