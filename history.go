// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const historyRetentionDays = 7

// dayFile is the on-disk format for one day of history.
type dayFile struct {
	MachineID  string            `json:"machine_id"`
	Date       string            `json:"date"`
	Components map[string]string `json:"components"` // component -> worst status that day
}

// HistoryStore persists per-component daily status to disk.
// One JSON file per day, stored in <config-dir>/history/.
type HistoryStore struct {
	dir       string
	machineID string
}

// NewHistoryStore creates a store rooted next to the config file.
func NewHistoryStore(configPath string) *HistoryStore {
	return &HistoryStore{
		dir:       filepath.Join(filepath.Dir(configPath), "history"),
		machineID: readMachineID(),
	}
}

// readMachineID returns the system's unique machine identifier.
// Falls back to hostname if /etc/machine-id is unavailable.
func readMachineID() string {
	data, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		h, _ := os.Hostname()
		return h
	}
	return strings.TrimSpace(string(data))
}

// Load reads all valid history files and returns component history.
//
// Files whose machine_id does not match the current host are silently
// ignored — this handles the case where the history directory is copied
// to a new server: the new machine gets a clean slate automatically.
//
// Files older than historyRetentionDays are deleted during this pass.
func (hs *HistoryStore) Load() map[string][]HistoryDay {
	result := make(map[string][]HistoryDay)

	entries, err := os.ReadDir(hs.dir)
	if err != nil {
		return result // directory doesn't exist yet — fine on first run
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -historyRetentionDays).Format("2006-01-02")

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		date := strings.TrimSuffix(entry.Name(), ".json")
		path := filepath.Join(hs.dir, entry.Name())

		// Delete files from day 8 and beyond.
		if date <= cutoff {
			os.Remove(path)
			continue
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var f dayFile
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}

		// Different machine — history doesn't apply to this server.
		if f.MachineID != hs.machineID {
			continue
		}

		for component, status := range f.Components {
			result[component] = append(result[component], HistoryDay{
				Date:   date,
				Status: status,
			})
		}
	}

	// Guarantee oldest-first ordering for each component.
	for comp := range result {
		sort.Slice(result[comp], func(i, j int) bool {
			return result[comp][i].Date < result[comp][j].Date
		})
	}

	return result
}

// Save atomically writes today's component statuses to disk.
// Components with no data yet (status "unknown") are omitted.
// Called after every check cycle — safe to call frequently.
func (hs *HistoryStore) Save(history map[string][]HistoryDay) error {
	if err := os.MkdirAll(hs.dir, 0755); err != nil {
		return err
	}

	today := time.Now().UTC().Format("2006-01-02")
	components := make(map[string]string)

	for component, days := range history {
		for _, day := range days {
			if day.Date == today && day.Status != "unknown" {
				components[component] = day.Status
				break
			}
		}
	}

	if len(components) == 0 {
		return nil // nothing recorded yet today
	}

	f := dayFile{
		MachineID:  hs.machineID,
		Date:       today,
		Components: components,
	}

	data, err := json.Marshal(f)
	if err != nil {
		return err
	}

	// Atomic write: temp file + rename to avoid partial reads.
	path := filepath.Join(hs.dir, today+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
