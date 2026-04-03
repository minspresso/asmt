// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"
)

type SSLChecker struct {
	domains      []string
	warnDays     int
	criticalDays int
	tr           *Translations
}

func NewSSLChecker(domains []string, warnDays, criticalDays int, tr *Translations) *SSLChecker {
	if warnDays <= 0 {
		warnDays = 30
	}
	if criticalDays <= 0 {
		criticalDays = 7
	}
	return &SSLChecker{
		domains:      domains,
		warnDays:     warnDays,
		criticalDays: criticalDays,
		tr:           tr,
	}
}

func (c *SSLChecker) Name() string { return "ssl" }

func (c *SSLChecker) Check(ctx context.Context) []CheckResult {
	results := make([]CheckResult, 0, len(c.domains))
	for _, domain := range c.domains {
		results = append(results, c.checkDomain(ctx, domain))
	}
	return results
}

func (c *SSLChecker) checkDomain(ctx context.Context, host string) CheckResult {
	component := "ssl-" + host

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	rawConn, err := dialer.DialContext(ctx, "tcp", host+":443")
	if err != nil {
		return CheckResult{
			Component: component,
			Status:    StatusCritical,
			Message:   fmt.Sprintf("TCP connection failed: %s", err.Error()),
			CheckedAt: time.Now(),
		}
	}
	tlsConn := tls.Client(rawConn, &tls.Config{ServerName: host})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return CheckResult{
			Component: component,
			Status:    StatusCritical,
			Message:   fmt.Sprintf("TLS handshake failed: %s", err.Error()),
			CheckedAt: time.Now(),
		}
	}
	conn := tlsConn
	if err != nil {
		return CheckResult{
			Component: component,
			Status:    StatusCritical,
			Message:   fmt.Sprintf("TLS connection failed: %s", err.Error()),
			CheckedAt: time.Now(),
		}
	}
	defer conn.Close()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return CheckResult{
			Component: component,
			Status:    StatusCritical,
			Message:   "no certificate returned",
			CheckedAt: time.Now(),
		}
	}

	cert := certs[0]
	expiry := cert.NotAfter.UTC()
	daysLeft := int(time.Until(expiry).Hours() / 24)

	status := StatusOK
	switch {
	case daysLeft <= 0:
		status = StatusCritical
	case daysLeft <= c.criticalDays:
		status = StatusCritical
	case daysLeft <= c.warnDays:
		status = StatusWarn
	}

	var message string
	switch {
	case daysLeft <= 0:
		message = fmt.Sprintf("EXPIRED on %s", expiry.Format("2006-01-02"))
	case daysLeft == 1:
		message = fmt.Sprintf("expires tomorrow (%s)", expiry.Format("2006-01-02"))
	default:
		message = fmt.Sprintf("expires in %d days (%s)", daysLeft, expiry.Format("2006-01-02"))
	}

	return CheckResult{
		Component: component,
		Status:    status,
		Message:   message,
		Details: map[string]string{
			"expiry":    expiry.Format("2006-01-02"),
			"days_left": strconv.Itoa(daysLeft),
			"issuer":    cert.Issuer.CommonName,
			"subject":   cert.Subject.CommonName,
		},
		CheckedAt: time.Now(),
	}
}
