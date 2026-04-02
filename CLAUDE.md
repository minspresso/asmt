# Server-Stat

Go server monitoring tool. Single static binary, ~9MB RSS, 50MB memory cap.

## Architecture

All `package main`. No sub-packages. ~3100 lines of Go.

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

check_nginx.go        - PID file + signal 0, nginx -t, HTTP probe
check_apache.go       - PID file + signal 0, apachectl -t, HTTP probe
check_linux.go        - Disk (Bavail), memory (/proc/meminfo), load (/proc/loadavg)
check_phpfpm.go       - /proc scan for process name, socket/TCP dial
check_mariadb.go      - Persistent sql.DB pool, ping + SELECT 1 + thread count
check_postgres.go     - Persistent sql.DB pool (lib/pq), ping + SELECT 1 + pg_stat_activity
check_redis.go        - Raw TCP RESP protocol, PING + AUTH + INFO (no extra dep)
check_http.go         - Custom HTTP endpoint probes (one checker per endpoint)
check_wordpress.go    - HTTP GET site + wp-cron + REST API
check_firewall.go     - TCP dial to configured ports on localhost
check_loadbalancer.go - GCP metadata server + optional LB IP probe

lang/en.yaml     - English translations (~220 keys)
lang/ko.yaml     - Korean translations
web/dashboard.html - Single-page dark dashboard, polls /api/status + /api/logs
install.sh       - Auto-detect distro, install binary + systemd/OpenRC service
uninstall.sh     - Stop service, remove binary/config/service files
```

## Key design decisions

- **Flat package**: Everything in `main`. Simple, no import ceremony.
- **Reads /proc directly**: No shelling out to df/free. Faster, no fork.
- **Persistent DB pools**: MariaDB and PostgreSQL use sql.DB (sync.Once init).
- **Redis raw TCP**: RESP protocol directly — no external Redis client dep.
- **HTTP endpoints**: One checker per configured endpoint; each has its own component key for independent healthz referencing.
- **Ring buffer for logs**: Fixed 200-entry cap. Bounded memory.
- **Alerts outside lock**: scheduler.runAll collects alerts, releases lock, THEN sends.
- **Healthz strict**: Returns unhealthy if any critical check has no data yet.
- **Binds to 127.0.0.1**: Default localhost-only.

## Config structure

```yaml
server:
  address: "127.0.0.1:8080"
language: "en"                # or "ko"
check_interval: "30s"

checks:
  http_server:
    type: "auto"              # "nginx", "apache", or "auto"
  mariadb:
    dsn: "${MARIADB_DSN}"     # user:pass@tcp(host:3306)/db
  postgresql:
    enabled: false
    dsn: "${POSTGRES_DSN}"    # postgres://user:pass@host/db?sslmode=disable
  redis:
    enabled: false
    addr: "127.0.0.1:6379"
    password: "${REDIS_PASSWORD}"  # omit or leave empty if no auth
  http_endpoints:
    - enabled: true
      name: "api-health"           # unique; component key = "http-api-health"
      url: "http://localhost/health"
      expect_status: [200]
      expect_body: "ok"            # optional substring
      timeout: "5s"
    - enabled: true
      name: "admin"
      url: "https://example.com/admin"
      expect_status: [200, 302]
      headers:
        Authorization: "Bearer ${API_TOKEN}"
  wordpress:
    tls_skip_verify: false    # true only for self-signed certs

logs:
  enabled: true
  buffer_size: 200
  files: []                   # empty = auto-detect

healthz:
  critical_checks: ["nginx", "phpfpm", "mariadb"]
  # Add "redis", "postgresql", "http-api-health" as needed
```

## Build and deploy

```bash
make build          # CGO_ENABLED=0, -ldflags="-s -w"
make dist           # creates .tar.gz with binary + scripts
sudo bash install.sh
sudo bash uninstall.sh
```

No Go needed on target server. Static binary, zero runtime deps.

## i18n

Translation files in `lang/*.yaml`. Embedded at build time via `go:embed`.
Adding a language: copy `lang/en.yaml` to `lang/<code>.yaml`, translate, rebuild.

## Testing locally

```bash
go run .
curl localhost:8080/api/status
curl localhost:8080/healthz
curl localhost:8080/api/logs
```

## Security notes

- Binds 127.0.0.1 by default (not 0.0.0.0)
- No authentication — rely on firewall/reverse proxy
- All secrets via `${ENV_VAR}` — never hardcode in config
- LBIP validated against URL injection (rejects `/` and `?`)
- TLS verify ON by default for WordPress and HTTP endpoint checkers
- Security headers on all responses; dashboard XSS-escaped
- Email subjects CRLF-sanitized
- Redis: password sent over loopback only; firewall Redis port 6379 from external access

## Known limitations

- PID-based checks can have false negatives on PID reuse after crash
- Firewall checker tests localhost TCP dial, not iptables rules
- Log watcher may miss lines during rotation (standard tail trade-off)
- PostgreSQL connection count requires `pg_monitor` role or superuser
- Redis checker opens a new TCP connection per interval (stateless by design)
- No authentication built in — use reverse proxy or firewall

## Dependencies

3 external: `github.com/go-sql-driver/mysql`, `github.com/lib/pq`, `gopkg.in/yaml.v3`
