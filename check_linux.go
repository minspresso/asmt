// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type LinuxChecker struct {
	DiskWarn     int
	DiskCritical int
	MemWarn      int
	MemCritical  int
	tr           *Translations
}

func (c *LinuxChecker) Name() string { return "linux" }

func (c *LinuxChecker) Check(ctx context.Context) []CheckResult {
	var results []CheckResult
	results = append(results, c.checkDisk()...)
	results = append(results, c.checkMemory())
	results = append(results, c.checkLoadAvg())
	return results
}

func (c *LinuxChecker) checkDisk() []CheckResult {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return []CheckResult{{
			Component: "linux-disk",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.linux_disk_read_error", err.Error()),
			CheckedAt: time.Now(),
		}}
	}

	seen := make(map[string]bool)
	var results []CheckResult

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mountPoint := fields[1]
		fsType := fields[2]

		switch fsType {
		case "ext4", "ext3", "xfs", "btrfs", "zfs", "vfat":
		default:
			continue
		}

		if seen[mountPoint] {
			continue
		}
		seen[mountPoint] = true

		var stat syscall.Statfs_t
		if err := syscall.Statfs(mountPoint, &stat); err != nil {
			continue
		}

		// BUG FIX: Use Bavail (user-available) instead of Bfree (includes root-reserved).
		// On ext4, ~5% is reserved for root. Bfree overstates free space for normal users.
		total := stat.Blocks * uint64(stat.Bsize)
		avail := stat.Bavail * uint64(stat.Bsize)
		used := total - (stat.Bfree * uint64(stat.Bsize))
		if total == 0 {
			continue
		}
		// Calculate percentage based on usable space (total - reserved + used)
		usable := used + avail
		pct := 0
		if usable > 0 {
			pct = int(used * 100 / usable)
		}

		status := StatusOK
		if pct >= c.DiskCritical {
			status = StatusCritical
		} else if pct >= c.DiskWarn {
			status = StatusWarn
		}

		results = append(results, CheckResult{
			Component: "linux-disk",
			Status:    status,
			Message:   c.tr.T("checks.linux_disk_usage", mountPoint, pct),
			Details: map[string]string{
				"mount":     mountPoint,
				"usage_pct": strconv.Itoa(pct),
				"total_gb":  fmt.Sprintf("%.1f", float64(total)/1e9),
				"avail_gb":  fmt.Sprintf("%.1f", float64(avail)/1e9),
			},
			CheckedAt: time.Now(),
		})
	}

	if len(results) == 0 {
		results = append(results, CheckResult{
			Component: "linux-disk",
			Status:    StatusOK,
			Message:   c.tr.T("checks.linux_disk_no_fs"),
			CheckedAt: time.Now(),
		})
	}
	return results
}

func (c *LinuxChecker) checkMemory() CheckResult {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return CheckResult{
			Component: "linux-memory",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.linux_mem_read_error", err.Error()),
			CheckedAt: time.Now(),
		}
	}

	info := make(map[string]uint64)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, _ := strconv.ParseUint(fields[1], 10, 64)
		info[key] = val
	}

	totalKB := info["MemTotal"]
	availKB := info["MemAvailable"]
	if totalKB == 0 {
		return CheckResult{
			Component: "linux-memory",
			Status:    StatusUnknown,
			Message:   c.tr.T("checks.linux_mem_parse_error"),
			CheckedAt: time.Now(),
		}
	}

	// Guard against underflow: availKB > totalKB can happen transiently
	if availKB > totalKB {
		availKB = totalKB
	}
	usedKB := totalKB - availKB
	pct := int(usedKB * 100 / totalKB)

	status := StatusOK
	if pct >= c.MemCritical {
		status = StatusCritical
	} else if pct >= c.MemWarn {
		status = StatusWarn
	}

	return CheckResult{
		Component: "linux-memory",
		Status:    status,
		Message:   c.tr.T("checks.linux_mem_usage", pct, usedKB/1024, totalKB/1024),
		Details: map[string]string{
			"usage_pct":    strconv.Itoa(pct),
			"total_mb":     strconv.FormatUint(totalKB/1024, 10),
			"available_mb": strconv.FormatUint(availKB/1024, 10),
		},
		CheckedAt: time.Now(),
	}
}

func (c *LinuxChecker) checkLoadAvg() CheckResult {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return CheckResult{
			Component: "linux-load",
			Status:    StatusCritical,
			Message:   c.tr.T("checks.linux_load_read_error", err.Error()),
			CheckedAt: time.Now(),
		}
	}

	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return CheckResult{
			Component: "linux-load",
			Status:    StatusUnknown,
			Message:   c.tr.T("checks.linux_load_parse_error"),
			CheckedAt: time.Now(),
		}
	}

	load1, _ := strconv.ParseFloat(fields[0], 64)
	load5, _ := strconv.ParseFloat(fields[1], 64)
	load15, _ := strconv.ParseFloat(fields[2], 64)
	numCPU := float64(runtime.NumCPU())

	status := StatusOK
	if load1 > numCPU*2 {
		status = StatusCritical
	} else if load1 > numCPU {
		status = StatusWarn
	}

	return CheckResult{
		Component: "linux-load",
		Status:    status,
		Message:   c.tr.T("checks.linux_load_info", load1, load5, load15, int(numCPU)),
		Details: map[string]string{
			"load_1m":  fields[0],
			"load_5m":  fields[1],
			"load_15m": fields[2],
			"num_cpus": strconv.Itoa(int(numCPU)),
		},
		CheckedAt: time.Now(),
	}
}
