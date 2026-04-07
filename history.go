// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"encoding/json"
	"io"
	"net/http"
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

// readMachineID returns a unique identifier for this VM instance.
//
// Priority:
//  1. GCP instance ID (changes when a new VM is created, even if the same
//     disk is reattached — exactly what we need to detect VM migration)
//  2. /etc/machine-id (lives on disk, unchanged across reboots but also
//     unchanged when the disk is moved to a new VM — not sufficient alone)
//  3. hostname as a last resort
func readMachineID() string {
	// Try GCP metadata server first (3-second timeout, non-blocking on non-GCP).
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest("GET",
		"http://metadata.google.internal/computeMetadata/v1/instance/id", nil)
	if err == nil {
		req.Header.Set("Metadata-Flavor", "Google")
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			if id := strings.TrimSpace(string(body)); id != "" {
				return "gcp-" + id
			}
		}
	}

	// Fall back to /etc/machine-id (stable across reboots on the same disk).
	data, err := os.ReadFile("/etc/machine-id")
	if err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}

	h, _ := os.Hostname()
	return h
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
//
// Data files are written with mode 0640 and the parent directory with
// mode 0750: root (owner) can read/write, members of the root group
// can read, everyone else is denied. These files contain machine ID,
// the list of installed services, and daily status rollups — not
// catastrophic to leak, but there's no reason for unprivileged users
// on a multi-user system to read them.
func (hs *HistoryStore) Save(history map[string][]HistoryDay) error {
	if err := os.MkdirAll(hs.dir, 0750); err != nil {
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
	if err := os.WriteFile(tmp, data, 0640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
