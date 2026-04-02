package main

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

// PostgreSQLChecker monitors a PostgreSQL database using a persistent connection pool.
// DSN format: postgres://user:password@host:5432/dbname?sslmode=disable
// Use ${POSTGRES_DSN} env var expansion to avoid hardcoding credentials.
type PostgreSQLChecker struct {
	checkerName string
	DSN         string
	tr          *Translations
	db          *sql.DB
	once        sync.Once
	initErr     error
}

func NewPostgreSQLChecker(name, dsn string, tr *Translations) *PostgreSQLChecker {
	if name == "" {
		name = "postgresql"
	}
	return &PostgreSQLChecker{checkerName: name, DSN: dsn, tr: tr}
}

func (c *PostgreSQLChecker) Name() string {
	if c.checkerName == "postgresql" {
		return "postgresql"
	}
	return "postgresql-" + c.checkerName
}

func (c *PostgreSQLChecker) initDB() error {
	c.once.Do(func() {
		db, err := sql.Open("postgres", c.DSN)
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
func (c *PostgreSQLChecker) Close() {
	if c.db != nil {
		c.db.Close()
	}
}

func (c *PostgreSQLChecker) Check(ctx context.Context) []CheckResult {
	if err := c.initDB(); err != nil {
		return []CheckResult{{
			Component: c.Name() + "-connection",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.postgres_open_error", err.Error()),
			CheckedAt: time.Now(),
		}}
	}

	var results []CheckResult

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := c.db.PingContext(pingCtx); err != nil {
		return []CheckResult{{
			Component: c.Name() + "-connection",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.postgres_ping_failed", err.Error()),
			CheckedAt: time.Now(),
		}}
	}

	results = append(results, CheckResult{
		Component: c.Name() + "-connection",
		Status:    StatusOK,
		Message:   c.tr.T("checks.postgres_connected"),
		CheckedAt: time.Now(),
	})

	// Basic query test
	var dummy int
	queryCtx, queryCancel := context.WithTimeout(ctx, 5*time.Second)
	defer queryCancel()

	if err := c.db.QueryRowContext(queryCtx, "SELECT 1").Scan(&dummy); err != nil {
		results = append(results, CheckResult{
			Component: c.Name() + "-query",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.postgres_query_failed", err.Error()),
			CheckedAt: time.Now(),
		})
		return results
	}

	results = append(results, CheckResult{
		Component: c.Name() + "-query",
		Status:    StatusOK,
		Message:   c.tr.T("checks.postgres_query_ok"),
		CheckedAt: time.Now(),
	})

	// Active connection count — requires pg_monitor role or superuser
	var activeConns int
	connCtx, connCancel := context.WithTimeout(ctx, 5*time.Second)
	defer connCancel()

	row := c.db.QueryRowContext(connCtx,
		"SELECT count(*) FROM pg_stat_activity WHERE state = 'active'")
	if err := row.Scan(&activeConns); err == nil {
		results = append(results, CheckResult{
			Component: c.Name() + "-connections",
			Status:    StatusOK,
			Message:   c.tr.T("checks.postgres_connections", activeConns),
			Details:   map[string]string{"active_connections": fmt.Sprintf("%d", activeConns)},
			CheckedAt: time.Now(),
		})
	}

	return results
}
