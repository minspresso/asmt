# Server-Stat

Go server monitoring tool. Single static binary, ~9MB RSS, 50MB memory cap.

## Architecture

All `package main`. No sub-packages. ~2900 lines of Go.

```
main.go          - Entry point, wires checkers/alerters/scheduler/server
config.go        - YAML config loading with ${ENV_VAR} expansion, validation
checker.go       - Checker interface, Status enum, CheckResult type
scheduler.go     - Runs all checkers concurrently on interval, tracks status transitions
server.go        - HTTP server: dashboard(/), status API, logs API, healthz, i18n
alert.go         - Alerter interface + LogAlerter, WebhookAlerter, EmailAlerter
i18n.go          - YAML-based translation system, embedded via go:embed
detect.go        - Auto-detects distro, HTTP server, PHP-FPM socket, log file paths
logwatch.go      - Tails log files, matches error patterns, ring buffer storage

check_nginx.go   - PID file + signal 0, nginx -t, HTTP probe
check_apache.go  - PID file + signal 0, apachectl -t, HTTP probe
check_linux.go   - Disk (Bavail), memory (/proc/meminfo), load (/proc/loadavg)
check_phpfpm.go  - /proc scan for process name, socket/TCP dial
check_mariadb.go - Persistent sql.DB pool, ping + SELECT 1 + thread count
check_wordpress.go - HTTP GET site + wp-cron + REST API
check_firewall.go  - TCP dial to configured ports on localhost
check_loadbalancer.go - GCP metadata server + optional LB IP probe

lang/en.yaml     - English translations (~200 keys)
lang/ko.yaml     - Korean translations
web/dashboard.html - Single-page dark dashboard, polls /api/status + /api/logs
install.sh       - Auto-detect distro, install binary + systemd/OpenRC service
uninstall.sh     - Stop service, remove binary/config/service files
```

## Key design decisions

- **Flat package**: Everything in `main`. Simple, no import ceremony for a focused tool.
- **Reads /proc directly**: No shelling out to df/free. Faster, no fork, no output parsing.
- **Persistent DB pool**: MariaDB uses sql.DB pool (sync.Once init), not open/close per check.
- **Ring buffer for logs**: Fixed 200-entry cap. Bounded memory, no unbounded growth.
- **Alerts outside lock**: scheduler.runAll collects alerts, releases lock, THEN sends. Prevents slow webhook/SMTP from blocking dashboard reads.
- **Healthz strict**: Returns unhealthy if any critical check has no data yet (unknown != passing).
- **Binds to 127.0.0.1**: Default is localhost-only. Must explicitly set 0.0.0.0 to expose.

## Config structure

```yaml
server:
  address: "127.0.0.1:8080"  # localhost-only by default
language: "en"                # or "ko"
check_interval: "30s"
checks:
  http_server:
    type: "auto"              # "auto", "nginx", or "apache"
  wordpress:
    tls_skip_verify: false    # only for self-signed certs
  mariadb:
    dsn: "${MARIADB_DSN}"     # env var expansion
logs:
  enabled: true
  buffer_size: 200
  files: []                   # empty = auto-detect
```

## Build and deploy

```bash
make build          # CGO_ENABLED=0, -ldflags="-s -w", produces 7.3MB binary
make dist           # creates .tar.gz with binary + scripts
sudo bash install.sh   # auto-detects everything, installs service
sudo bash uninstall.sh # clean removal
```

No Go needed on target server. Static binary, zero deps.

## i18n

Translation files in `lang/*.yaml`. Embedded at build time via `go:embed`.
Adding a language: copy `lang/en.yaml` to `lang/<code>.yaml`, translate, rebuild.
Dashboard loads translations from `/api/i18n`. All UI strings come from YAML.

## Testing locally

```bash
go run .                      # uses config.yaml in current dir
go run . -config /path/to.yaml
curl localhost:8080/api/status
curl localhost:8080/healthz
curl localhost:8080/api/logs
```

## Security notes

- Binds 127.0.0.1 by default (not 0.0.0.0)
- No authentication on endpoints - rely on firewall/reverse proxy for access control
- Config supports ${ENV_VAR} for secrets (DSN, SMTP password, webhook URL)
- Security headers on all responses (X-Content-Type-Options, X-Frame-Options, etc.)
- Email subjects sanitized against CRLF injection
- Dashboard escapes all dynamic values (XSS prevention)
- LBIP validated to prevent SSRF (rejects URLs, allows only IP/host:port)
- TLS verification ON by default for WordPress checker

## Known limitations

- PID-based process checks can have false negatives if PID is reused after crash
- Firewall checker tests localhost TCP dial, not actual iptables rules
- Log watcher may miss lines during log rotation (standard tail trade-off)
- No authentication built in - use reverse proxy (nginx) or firewall rules

## Dependencies

Only 2 external: `github.com/go-sql-driver/mysql`, `gopkg.in/yaml.v3`
