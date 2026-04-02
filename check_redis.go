// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// RedisChecker verifies Redis connectivity via raw TCP (RESP protocol).
// No external dependency required.
type RedisChecker struct {
	checkerName string
	addr        string
	password    string
	tr          *Translations
}

func NewRedisChecker(name, addr, password string, tr *Translations) *RedisChecker {
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	if name == "" {
		name = "redis"
	}
	return &RedisChecker{checkerName: name, addr: addr, password: password, tr: tr}
}

func (c *RedisChecker) Name() string {
	if c.checkerName == "redis" {
		return "redis"
	}
	return "redis-" + c.checkerName
}

func (c *RedisChecker) comp(suffix string) string {
	return c.Name() + "-" + suffix
}

func (c *RedisChecker) Check(ctx context.Context) []CheckResult {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", c.addr)
	if err != nil {
		return []CheckResult{{
			Component: c.comp("connection"),
			Status:    StatusCritical,
			Message:   c.tr.T("checks.redis_conn_failed", c.addr, err.Error()),
			CheckedAt: time.Now(),
		}}
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	r := bufio.NewReader(conn)

	// Authenticate if password is configured
	if c.password != "" {
		fmt.Fprintf(conn, "*2\r\n$4\r\nAUTH\r\n$%d\r\n%s\r\n", len(c.password), c.password)
		line, err := r.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "+OK") {
			msg := strings.TrimSpace(line)
			if err != nil {
				msg = err.Error()
			}
			return []CheckResult{{
				Component: c.comp("connection"),
				Status:    StatusCritical,
				Message:   c.tr.T("checks.redis_auth_failed", msg),
				CheckedAt: time.Now(),
			}}
		}
	}

	// PING
	fmt.Fprintf(conn, "*1\r\n$4\r\nPING\r\n")
	line, err := r.ReadString('\n')
	if err != nil {
		return []CheckResult{{
			Component: c.comp("connection"),
			Status:    StatusCritical,
			Message:   c.tr.T("checks.redis_conn_failed", c.addr, err.Error()),
			CheckedAt: time.Now(),
		}}
	}

	if strings.TrimSpace(line) != "+PONG" {
		return []CheckResult{{
			Component: c.comp("connection"),
			Status:    StatusWarn,
			Message:   c.tr.T("checks.redis_unexpected", strings.TrimSpace(line)),
			CheckedAt: time.Now(),
		}}
	}

	results := []CheckResult{{
		Component: c.comp("connection"),
		Status:    StatusOK,
		Message:   c.tr.T("checks.redis_pong_ok", c.addr),
		CheckedAt: time.Now(),
	}}

	// INFO — parse used_memory_human and connected_clients
	fmt.Fprintf(conn, "*1\r\n$4\r\nINFO\r\n")
	header, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(header, "$") {
		return results
	}

	lenStr := strings.TrimSpace(strings.TrimPrefix(header, "$"))
	byteCount, err := strconv.Atoi(lenStr)
	if err != nil || byteCount <= 0 {
		return results
	}

	infoBytes := make([]byte, byteCount)
	if _, err := io.ReadFull(r, infoBytes); err != nil {
		return results
	}

	var usedMemory, connectedClients string
	for _, rawLine := range strings.Split(string(infoBytes), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(rawLine), ":"); ok {
			switch k {
			case "used_memory_human":
				usedMemory = v
			case "connected_clients":
				connectedClients = v
			}
		}
	}

	if usedMemory != "" {
		results = append(results, CheckResult{
			Component: c.comp("memory"),
			Status:    StatusOK,
			Message:   c.tr.T("checks.redis_memory", usedMemory),
			CheckedAt: time.Now(),
		})
	}
	if connectedClients != "" {
		results = append(results, CheckResult{
			Component: c.comp("clients"),
			Status:    StatusOK,
			Message:   c.tr.T("checks.redis_clients", connectedClients),
			CheckedAt: time.Now(),
		})
	}

	return results
}
