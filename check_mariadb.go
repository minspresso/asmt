package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type MariaDBChecker struct {
	DSN string
}

func (c *MariaDBChecker) Name() string { return "mariadb" }

func (c *MariaDBChecker) Check(ctx context.Context) []CheckResult {
	var results []CheckResult

	db, err := sql.Open("mysql", c.DSN)
	if err != nil {
		return []CheckResult{{
			Component: "mariadb-connection",
			Status:    StatusCritical,
			Message:   "cannot open connection: " + err.Error(),
			CheckedAt: time.Now(),
		}}
	}
	defer db.Close()

	db.SetConnMaxLifetime(5 * time.Second)
	db.SetMaxOpenConns(1)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		return []CheckResult{{
			Component: "mariadb-connection",
			Status:    StatusCritical,
			Message:   "ping failed: " + err.Error(),
			CheckedAt: time.Now(),
		}}
	}

	results = append(results, CheckResult{
		Component: "mariadb-connection",
		Status:    StatusOK,
		Message:   "connected successfully",
		CheckedAt: time.Now(),
	})

	var result int
	queryCtx, queryCancel := context.WithTimeout(ctx, 5*time.Second)
	defer queryCancel()

	if err := db.QueryRowContext(queryCtx, "SELECT 1").Scan(&result); err != nil {
		results = append(results, CheckResult{
			Component: "mariadb-query",
			Status:    StatusCritical,
			Message:   "query failed: " + err.Error(),
			CheckedAt: time.Now(),
		})
	} else {
		results = append(results, CheckResult{
			Component: "mariadb-query",
			Status:    StatusOK,
			Message:   "query executed successfully",
			CheckedAt: time.Now(),
		})
	}

	var threadsConnected string
	statusCtx, statusCancel := context.WithTimeout(ctx, 5*time.Second)
	defer statusCancel()

	row := db.QueryRowContext(statusCtx, "SHOW STATUS LIKE 'Threads_connected'")
	var varName string
	if err := row.Scan(&varName, &threadsConnected); err == nil {
		results = append(results, CheckResult{
			Component: "mariadb-threads",
			Status:    StatusOK,
			Message:   fmt.Sprintf("%s active connections", threadsConnected),
			Details:   map[string]string{"threads_connected": threadsConnected},
			CheckedAt: time.Now(),
		})
	}

	return results
}
