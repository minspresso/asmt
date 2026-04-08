// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2025 minspresso

package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Language      string              `yaml:"language"`
	CheckInterval Duration            `yaml:"check_interval"`
	Checks        ChecksConfig        `yaml:"checks"`
	Logs          LogsConfig          `yaml:"logs"`
	Healthz       HealthzConfig       `yaml:"healthz"`
	Alerts        AlertsConfig        `yaml:"alerts"`
}

type LogsConfig struct {
	Enabled    bool     `yaml:"enabled"`
	BufferSize int      `yaml:"buffer_size"`
	Files      []string `yaml:"files"`
}

type ServerConfig struct {
	Address string `yaml:"address"`
}

type ChecksConfig struct {
	LoadBalancer    LBCheckConfig         `yaml:"loadbalancer"`
	Linux           LinuxCheckConfig      `yaml:"linux"`
	Firewall        FirewallCheckConfig   `yaml:"firewall"`
	HTTPServer      HTTPServerCheckConfig `yaml:"http_server"`
	PHPFPM          PHPFPMCheckConfig     `yaml:"phpfpm"`
	MariaDB         MariaDBCheckConfig    `yaml:"mariadb"`
	WordPress       WordPressCheckConfig  `yaml:"wordpress"`
	Redis           RedisCheckConfig      `yaml:"redis"`
	PostgreSQL      PostgreSQLCheckConfig `yaml:"postgresql"`
	HTTPEndpoints   []HTTPEndpointConfig  `yaml:"http_endpoints"`
	SSLCertificates SSLConfig             `yaml:"ssl_certificates"`
}

// SSLConfig configures TLS certificate expiry checks.
// Domains are checked on port 443. Hosts are also auto-collected
// from any https:// WordPress or HTTP endpoint URLs in the config.
type SSLConfig struct {
	Enabled      bool     `yaml:"enabled"`
	WarnDays     int      `yaml:"warn_days"`     // default 30
	CriticalDays int      `yaml:"critical_days"` // default 7
	Domains      []string `yaml:"domains"`       // explicit list; merged with auto-detected
}

type HTTPServerCheckConfig struct {
	Enabled bool   `yaml:"enabled"`
	Type    string `yaml:"type"`    // "nginx", "apache", or "auto" (default)
	PIDFile string `yaml:"pid_file"` // override auto-detected PID file
}

type LBCheckConfig struct {
	Enabled bool   `yaml:"enabled"`
	LBIP    string `yaml:"lb_ip"`
}

type LinuxCheckConfig struct {
	Enabled      bool `yaml:"enabled"`
	DiskWarn     int  `yaml:"disk_warn"`
	DiskCritical int  `yaml:"disk_critical"`
	MemWarn      int  `yaml:"mem_warn"`
	MemCritical  int  `yaml:"mem_critical"`
}

type FirewallCheckConfig struct {
	Enabled bool  `yaml:"enabled"`
	Ports   []int `yaml:"ports"`
}

type PHPFPMCheckConfig struct {
	Enabled bool   `yaml:"enabled"`
	Socket  string `yaml:"socket"`
	Port    int    `yaml:"port"`
}

type MariaDBCheckConfig struct {
	Enabled bool   `yaml:"enabled"`
	DSN     string `yaml:"dsn"`
}

type WordPressCheckConfig struct {
	Enabled       bool   `yaml:"enabled"`
	URL           string `yaml:"url"`
	ExpectBody    string `yaml:"expect_body"`
	TLSSkipVerify bool   `yaml:"tls_skip_verify"` // default false; set true only for self-signed certs
}

type RedisCheckConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Name     string `yaml:"name"`     // display name; defaults to "redis"
	Addr     string `yaml:"addr"`     // host:port; defaults to "127.0.0.1:6379"
	Password string `yaml:"password"` // use ${REDIS_PASSWORD}, never hardcode
}

type PostgreSQLCheckConfig struct {
	Enabled bool   `yaml:"enabled"`
	Name    string `yaml:"name"` // display name; defaults to "postgresql"
	DSN     string `yaml:"dsn"`  // postgres://user:pass@host/db?sslmode=disable, use ${POSTGRES_DSN}
}

// HTTPEndpointConfig describes a single custom HTTP health probe.
// Multiple entries are supported under checks.http_endpoints.
type HTTPEndpointConfig struct {
	Enabled       bool              `yaml:"enabled"`
	Name          string            `yaml:"name"`           // unique name; used as component key
	URL           string            `yaml:"url"`
	Method        string            `yaml:"method"`         // GET by default
	Headers       map[string]string `yaml:"headers"`
	ExpectStatus  []int             `yaml:"expect_status"`  // default [200]
	ExpectBody    string            `yaml:"expect_body"`    // optional substring check
	TLSSkipVerify bool              `yaml:"tls_skip_verify"`
	Timeout       Duration          `yaml:"timeout"`        // default 10s
}

type HealthzConfig struct {
	CriticalChecks []string `yaml:"critical_checks"`
}

type AlertsConfig struct {
	Log     LogAlertConfig     `yaml:"log"`
	Webhook WebhookAlertConfig `yaml:"webhook"`
	Email   EmailAlertConfig   `yaml:"email"`
}

type LogAlertConfig struct {
	Enabled bool `yaml:"enabled"`
}

type WebhookAlertConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
}

type EmailAlertConfig struct {
	Enabled  bool     `yaml:"enabled"`
	SMTPHost string   `yaml:"smtp_host"`
	SMTPPort int      `yaml:"smtp_port"`
	From     string   `yaml:"from"`
	To       []string `yaml:"to"`
	Username string   `yaml:"username"`
	Password string   `yaml:"password"`
}

// Duration wraps time.Duration for YAML unmarshaling.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

// expandEnv replaces ${VAR} and $VAR references in a string with
// environment variable values. This allows sensitive values like
// database passwords to be kept out of config files.
func expandEnv(s string) string {
	return os.ExpandEnv(s)
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Expand environment variables before parsing
	expanded := expandEnv(string(data))

	cfg := &Config{
		Server:        ServerConfig{Address: "127.0.0.1:8080"},
		Language:      "en",
		CheckInterval: Duration{30 * time.Second},
		Checks: ChecksConfig{
			LoadBalancer: LBCheckConfig{Enabled: true},
			Linux:        LinuxCheckConfig{Enabled: true, DiskWarn: 80, DiskCritical: 90, MemWarn: 85, MemCritical: 95},
			Firewall:     FirewallCheckConfig{Enabled: true, Ports: []int{80, 443, 3306}},
			HTTPServer:   HTTPServerCheckConfig{Enabled: true, Type: "auto"},
			PHPFPM:       PHPFPMCheckConfig{Enabled: true},
			MariaDB:      MariaDBCheckConfig{Enabled: true},
			WordPress:    WordPressCheckConfig{Enabled: true, URL: "http://localhost", ExpectBody: "</html>"},
		},
		Logs:    LogsConfig{Enabled: true, BufferSize: 200},
		Healthz: HealthzConfig{CriticalChecks: []string{"nginx", "phpfpm", "mariadb"}},
		Alerts: AlertsConfig{
			Log: LogAlertConfig{Enabled: true},
		},
	}

	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, err
	}

	// Validate: check interval must be positive
	if cfg.CheckInterval.Duration <= 0 {
		cfg.CheckInterval.Duration = 30 * time.Second
	}

	// Validate: LBIP must be a bare IP or host:port, not a full URL
	if ip := cfg.Checks.LoadBalancer.LBIP; ip != "" {
		if strings.Contains(ip, "/") || strings.Contains(ip, "?") {
			return nil, fmt.Errorf("lb_ip must be an IP or host:port, not a URL: %q", ip)
		}
	}

	// Warn about sensitive defaults
	if cfg.Checks.MariaDB.Enabled && cfg.Checks.MariaDB.DSN != "" {
		if strings.Contains(cfg.Checks.MariaDB.DSN, "password@") {
			slog.Warn("MariaDB DSN appears to contain a default password. Use environment variables: dsn: ${MARIADB_DSN}")
		}
	}

	return cfg, nil
}
