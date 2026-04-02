// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"time"
)

// Status represents the health status of a component.
type Status int

const (
	StatusOK Status = iota
	StatusWarn
	StatusCritical
	StatusUnknown
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarn:
		return "warn"
	case StatusCritical:
		return "critical"
	case StatusUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// CheckResult holds the outcome of a single health check.
type CheckResult struct {
	Component string            `json:"component"`
	Status    Status            `json:"status"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
	CheckedAt time.Time         `json:"checked_at"`
	Duration  time.Duration     `json:"duration"`
}

// Checker is the interface that all health checkers implement.
type Checker interface {
	Name() string
	Check(ctx context.Context) []CheckResult
}
