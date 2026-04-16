// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// readConfigSafely opens a file with O_NOFOLLOW so symlinks are not
// followed, then reads up to maxSize bytes. Refusing to follow symlinks
// prevents a local attacker who gains write access to /etc/nginx/ from
// pointing a "config file" at /etc/shadow or similar sensitive paths.
func readConfigSafely(path string, maxSize int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxSize))
}

// nginxDomains returns domains from nginx config files that listen on 443/ssl.
// It reads config files directly from /etc/nginx/ to avoid issues with
// systemd sandboxing (ProtectSystem=strict blocks nginx -T).
// Returns an empty slice if nginx config is not found.
func nginxDomains() []string {
	var allData []byte

	// Read all config files from standard nginx paths.
	paths := []string{
		"/etc/nginx/nginx.conf",
	}
	// Also read all included site configs.
	for _, glob := range []string{
		"/etc/nginx/sites-enabled/*",
		"/etc/nginx/conf.d/*.conf",
	} {
		matches, _ := filepath.Glob(glob)
		paths = append(paths, matches...)
	}

	// Per-file read cap. Nginx configs are small text files; anything
	// above this limit is almost certainly malicious or broken.
	const maxConfigSize = 2 * 1024 * 1024 // 2 MB

	for _, p := range paths {
		data, err := readConfigSafely(p, maxConfigSize)
		if err != nil {
			continue
		}
		allData = append(allData, '\n')
		allData = append(allData, data...)
	}

	if len(allData) == 0 {
		slog.Debug("no nginx config files found, skipping domain auto-detection")
		return nil
	}

	return parseNginxDomains(allData)
}

type serverBlock struct {
	hasSSL      bool
	serverNames []string
}

// parseNginxDomains extracts domains from nginx -T output.
// It tracks server blocks (brace depth) and collects server_name values
// from blocks that have a listen directive with 443 or ssl.
func parseNginxDomains(data []byte) []string {
	var blocks []serverBlock
	var current *serverBlock
	depth := 0
	blockStartDepth := -1

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Track brace depth
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")

		// Detect "server {" block start
		if strings.HasPrefix(line, "server") && strings.Contains(line, "{") && !strings.HasPrefix(line, "server_name") {
			current = &serverBlock{}
			blockStartDepth = depth
		}

		depth += opens - closes

		if current == nil {
			continue
		}

		// Parse listen directive for SSL
		if strings.HasPrefix(line, "listen") {
			rest := strings.TrimPrefix(line, "listen")
			rest = strings.TrimRight(rest, ";")
			if strings.Contains(rest, "443") || strings.Contains(rest, "ssl") {
				current.hasSSL = true
			}
		}

		// Parse server_name directive
		if strings.HasPrefix(line, "server_name") {
			rest := strings.TrimPrefix(line, "server_name")
			rest = strings.TrimRight(rest, ";")
			current.serverNames = append(current.serverNames, strings.Fields(rest)...)
		}

		// End of server block
		if depth <= blockStartDepth {
			blocks = append(blocks, *current)
			current = nil
			blockStartDepth = -1
		}
	}

	domains := collectSSLDomains(blocks)
	if len(domains) > 0 {
		slog.Info("auto-detected SSL domains from nginx", "domains", domains)
	}
	return domains
}

// collectSSLDomains deduplicates and filters valid domain names from SSL server blocks.
func collectSSLDomains(blocks []serverBlock) []string {
	seen := make(map[string]struct{})
	var domains []string
	for _, b := range blocks {
		if !b.hasSSL {
			continue
		}
		for _, name := range b.serverNames {
			name = strings.TrimSpace(name)
			if !isValidSSLDomain(name) {
				continue
			}
			if _, dup := seen[name]; !dup {
				seen[name] = struct{}{}
				domains = append(domains, name)
			}
		}
	}
	return domains
}

// isValidSSLDomain returns true if the name is a real domain worth checking.
// Filters out placeholders, localhost, IPs, and wildcard entries.
func isValidSSLDomain(name string) bool {
	if name == "" || name == "_" || name == "localhost" {
		return false
	}
	// Skip wildcard entries like *.example.com
	if strings.HasPrefix(name, "*.") {
		return false
	}
	// Skip IP addresses
	if net.ParseIP(name) != nil {
		return false
	}
	// Must contain at least one dot (valid domain)
	if !strings.Contains(name, ".") {
		return false
	}
	return true
}
