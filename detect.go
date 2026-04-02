// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Distro represents a Linux distribution family.
type Distro int

const (
	DistroUnknown Distro = iota
	DistroDebian         // Debian, Ubuntu, Mint
	DistroRHEL           // RHEL, CentOS, Rocky, AlmaLinux, Fedora
	DistroArch           // Arch, Manjaro
	DistroAlpine         // Alpine
	DistroSUSE           // openSUSE, SLES
)

func (d Distro) String() string {
	switch d {
	case DistroDebian:
		return "debian"
	case DistroRHEL:
		return "rhel"
	case DistroArch:
		return "arch"
	case DistroAlpine:
		return "alpine"
	case DistroSUSE:
		return "suse"
	default:
		return "unknown"
	}
}

// DetectDistro determines the Linux distribution family.
func DetectDistro() Distro {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return DistroUnknown
	}
	content := strings.ToLower(string(data))

	switch {
	case strings.Contains(content, "id=debian") ||
		strings.Contains(content, "id=ubuntu") ||
		strings.Contains(content, "id_like=debian") ||
		strings.Contains(content, "id=linuxmint"):
		return DistroDebian
	case strings.Contains(content, "id=rhel") ||
		strings.Contains(content, "id=centos") ||
		strings.Contains(content, "id=fedora") ||
		strings.Contains(content, "id=rocky") ||
		strings.Contains(content, "id=almalinux") ||
		strings.Contains(content, "id_like=rhel") ||
		strings.Contains(content, `id_like="rhel`):
		return DistroRHEL
	case strings.Contains(content, "id=arch") ||
		strings.Contains(content, "id=manjaro") ||
		strings.Contains(content, "id_like=arch"):
		return DistroArch
	case strings.Contains(content, "id=alpine"):
		return DistroAlpine
	case strings.Contains(content, "id=opensuse") ||
		strings.Contains(content, "id=sles") ||
		strings.Contains(content, "id_like=suse"):
		return DistroSUSE
	default:
		return DistroUnknown
	}
}

// ServiceInfo describes an auto-detected service.
type ServiceInfo struct {
	Name      string
	Installed bool
	Binary    string // full path to binary, if found
}

// DetectService checks if a binary exists in common locations.
func DetectService(names ...string) ServiceInfo {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return ServiceInfo{Name: name, Installed: true, Binary: path}
		}
	}
	return ServiceInfo{Name: names[0], Installed: false}
}

// DetectHTTPServer returns "nginx", "apache", or "" based on what's installed.
func DetectHTTPServer() string {
	if s := DetectService("nginx"); s.Installed {
		return "nginx"
	}
	if s := DetectService("apache2", "httpd", "apache2ctl", "apachectl"); s.Installed {
		return "apache"
	}
	return ""
}

// NginxPIDPaths returns candidate PID file paths across distros.
func NginxPIDPaths() []string {
	return []string{
		"/run/nginx.pid",
		"/var/run/nginx.pid",
		"/tmp/nginx.pid",
	}
}

// FindNginxPID returns the first PID file that exists.
func FindNginxPID() string {
	for _, p := range NginxPIDPaths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/run/nginx.pid" // fallback
}

// ApacheConfigTestCmd returns the command to test Apache config.
func ApacheConfigTestCmd() (string, []string) {
	for _, name := range []string{"apache2ctl", "apachectl", "httpd"} {
		if _, err := exec.LookPath(name); err == nil {
			return name, []string{"-t"}
		}
	}
	return "apachectl", []string{"-t"}
}

// ApachePIDPaths returns candidate PID file paths across distros.
func ApachePIDPaths() []string {
	return []string{
		"/run/apache2/apache2.pid",   // Debian/Ubuntu
		"/run/httpd/httpd.pid",       // RHEL/CentOS
		"/var/run/apache2/apache2.pid",
		"/var/run/httpd/httpd.pid",
		"/run/apache2.pid",
		"/run/httpd.pid",
	}
}

// FindApachePID returns the first Apache PID file that exists.
func FindApachePID() string {
	for _, p := range ApachePIDPaths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// PHPFPMSocketPaths returns candidate socket paths across distros and PHP versions.
func PHPFPMSocketPaths() []string {
	var paths []string

	// Debian/Ubuntu pattern: /run/php/phpX.Y-fpm.sock
	matches, _ := filepath.Glob("/run/php/php*-fpm.sock")
	paths = append(paths, matches...)

	// RHEL/CentOS/Fedora pattern
	paths = append(paths,
		"/run/php-fpm/www.sock",
		"/var/run/php-fpm/www.sock",
	)

	// Arch pattern
	paths = append(paths, "/run/php-fpm/php-fpm.sock")

	// Alpine pattern
	paths = append(paths, "/run/php-fpm.sock", "/var/run/php-fpm.sock")

	// SUSE
	paths = append(paths, "/run/php-fpm/php-fpm.sock")

	return paths
}

// FindPHPFPMSocket returns the first PHP-FPM socket that exists.
func FindPHPFPMSocket() string {
	for _, p := range PHPFPMSocketPaths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/run/php/php-fpm.sock" // fallback
}

// PHPFPMProcessNames returns process names to look for across distros.
// On RHEL, the process may be "php-fpm" or "php-fpm-8.2" etc.
func PHPFPMProcessNames() []string {
	return []string{"php-fpm"}
}

// DetectLogFiles returns log files that actually exist on this system.
func DetectLogFiles() []LogFileConfig {
	candidates := []LogFileConfig{
		// Nginx
		{Path: "/var/log/nginx/error.log", Source: "nginx"},

		// Apache (Debian and RHEL paths)
		{Path: "/var/log/apache2/error.log", Source: "apache"},
		{Path: "/var/log/httpd/error_log", Source: "apache"},

		// PHP-FPM: try versioned (Debian) and unversioned (RHEL) paths
		{Path: "/var/log/php-fpm/www-error.log", Source: "php-fpm"},
		{Path: "/var/log/php-fpm/error.log", Source: "php-fpm"},

		// MariaDB / MySQL
		{Path: "/var/log/mysql/error.log", Source: "mariadb"},
		{Path: "/var/log/mariadb/mariadb.log", Source: "mariadb"},
		{Path: "/var/log/mariadb/error.log", Source: "mariadb"},
		{Path: "/var/log/mysqld.log", Source: "mariadb"},

		// System logs
		{Path: "/var/log/syslog", Source: "system"},   // Debian/Ubuntu
		{Path: "/var/log/messages", Source: "system"},  // RHEL/CentOS/SUSE
		{Path: "/var/log/kern.log", Source: "system"},  // Debian kernel log
	}

	// Also glob for versioned PHP-FPM logs (Debian: /var/log/phpX.Y-fpm.log)
	phpGlob, _ := filepath.Glob("/var/log/php*-fpm.log")
	for _, p := range phpGlob {
		candidates = append(candidates, LogFileConfig{Path: p, Source: "php-fpm"})
	}

	// Return only files that exist
	var found []LogFileConfig
	seen := make(map[string]bool)
	for _, c := range candidates {
		if seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		if _, err := os.Stat(c.Path); err == nil {
			found = append(found, c)
		}
	}
	return found
}
