// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type MariaDBChecker struct {
	DSN      string
	tr       *Translations
	db       *sql.DB
	once     sync.Once
	initErr  error
	disabled bool // set after first connection failure to stop retrying
}

// NewMariaDBChecker creates a checker with a persistent connection pool.
// The pool is initialized lazily on first Check call.
func NewMariaDBChecker(dsn string, tr *Translations) *MariaDBChecker {
	return &MariaDBChecker{DSN: dsn, tr: tr}
}

func (c *MariaDBChecker) Name() string { return "mariadb" }

func (c *MariaDBChecker) initDB() error {
	c.once.Do(func() {
		db, err := sql.Open("mysql", c.DSN)
		if err != nil {
			c.initErr = err
			return
		}
		db.SetConnMaxLifetime(5 * time.Minute)
		db.SetMaxOpenConns(2)
		db.SetMaxIdleConns(1)
		c.db = db
	})
	return c.initErr
}

// Close releases the database connection pool. Call on shutdown.
func (c *MariaDBChecker) Close() {
	if c.db != nil {
		c.db.Close()
	}
}

func (c *MariaDBChecker) Check(ctx context.Context) []CheckResult {
	if c.disabled {
		return []CheckResult{{
			Component: "mariadb-connection",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.mariadb_not_configured"),
			CheckedAt: time.Now(),
		}}
	}

	if err := c.initDB(); err != nil {
		c.disabled = true
		slog.Warn("MariaDB check disabled after connection pool error, will not retry", "error", err)
		return []CheckResult{{
			Component: "mariadb-connection",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.mariadb_not_configured"),
			CheckedAt: time.Now(),
		}}
	}

	var results []CheckResult

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := c.db.PingContext(pingCtx); err != nil {
		c.disabled = true
		slog.Warn("MariaDB check disabled after ping failure, will not retry", "error", err)
		return []CheckResult{{
			Component: "mariadb-connection",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.mariadb_not_configured"),
			CheckedAt: time.Now(),
		}}
	}

	results = append(results, CheckResult{
		Component: "mariadb-connection",
		Status:    StatusOK,
		Message:   c.tr.T("checks.mariadb_connected"),
		CheckedAt: time.Now(),
	})

	var result int
	queryCtx, queryCancel := context.WithTimeout(ctx, 5*time.Second)
	defer queryCancel()

	if err := c.db.QueryRowContext(queryCtx, "SELECT 1").Scan(&result); err != nil {
		results = append(results, CheckResult{
			Component: "mariadb-query",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.mariadb_query_failed", err.Error()),
			CheckedAt: time.Now(),
		})
	} else {
		results = append(results, CheckResult{
			Component: "mariadb-query",
			Status:    StatusOK,
			Message:   c.tr.T("checks.mariadb_query_ok"),
			CheckedAt: time.Now(),
		})
	}

	var threadsConnected string
	statusCtx, statusCancel := context.WithTimeout(ctx, 5*time.Second)
	defer statusCancel()

	row := c.db.QueryRowContext(statusCtx, "SHOW STATUS LIKE 'Threads_connected'")
	var varName string
	if err := row.Scan(&varName, &threadsConnected); err == nil {
		results = append(results, CheckResult{
			Component: "mariadb-threads",
			Status:    StatusOK,
			Message:   c.tr.T("checks.mariadb_threads", threadsConnected),
			Details:   map[string]string{"threads_connected": threadsConnected},
			CheckedAt: time.Now(),
		})
	}

	return results
}
