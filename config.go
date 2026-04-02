package main

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	CheckInterval Duration            `yaml:"check_interval"`
	Checks        ChecksConfig        `yaml:"checks"`
	Healthz       HealthzConfig       `yaml:"healthz"`
	Alerts        AlertsConfig        `yaml:"alerts"`
}

type ServerConfig struct {
	Address string `yaml:"address"`
}

type ChecksConfig struct {
	LoadBalancer LBCheckConfig        `yaml:"loadbalancer"`
	Linux        LinuxCheckConfig     `yaml:"linux"`
	Firewall     FirewallCheckConfig  `yaml:"firewall"`
	Nginx        NginxCheckConfig     `yaml:"nginx"`
	PHPFPM       PHPFPMCheckConfig    `yaml:"phpfpm"`
	MariaDB      MariaDBCheckConfig   `yaml:"mariadb"`
	WordPress    WordPressCheckConfig `yaml:"wordpress"`
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

type NginxCheckConfig struct {
	Enabled  bool     `yaml:"enabled"`
	PIDFile  string   `yaml:"pid_file"`
	Interval Duration `yaml:"interval"`
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
	Enabled    bool   `yaml:"enabled"`
	URL        string `yaml:"url"`
	ExpectBody string `yaml:"expect_body"`
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

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Server:        ServerConfig{Address: ":8080"},
		CheckInterval: Duration{30 * time.Second},
		Checks: ChecksConfig{
			LoadBalancer: LBCheckConfig{Enabled: true},
			Linux:        LinuxCheckConfig{Enabled: true, DiskWarn: 80, DiskCritical: 90, MemWarn: 85, MemCritical: 95},
			Firewall:     FirewallCheckConfig{Enabled: true, Ports: []int{80, 443, 3306}},
			Nginx:        NginxCheckConfig{Enabled: true, PIDFile: "/run/nginx.pid"},
			PHPFPM:       PHPFPMCheckConfig{Enabled: true, Socket: "/run/php/php-fpm.sock"},
			MariaDB:      MariaDBCheckConfig{Enabled: true, DSN: "monitor:password@tcp(127.0.0.1:3306)/mysql"},
			WordPress:    WordPressCheckConfig{Enabled: true, URL: "http://localhost", ExpectBody: "</html>"},
		},
		Healthz: HealthzConfig{CriticalChecks: []string{"nginx", "phpfpm", "mariadb"}},
		Alerts: AlertsConfig{
			Log: LogAlertConfig{Enabled: true},
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}
