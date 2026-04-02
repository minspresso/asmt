# Server-Stat

A lightweight server monitoring tool built in Go for Debian/GCP servers running nginx, PHP-FPM, MariaDB, and WordPress.

## What it monitors

| Component | Checks |
|-----------|--------|
| **GCP Load Balancer** | Metadata server, LB path verification |
| **Linux OS** | Disk usage, memory, load average |
| **Firewall** | Port accessibility (80, 443, 3306) |
| **Nginx** | Process running, config valid, HTTP response |
| **PHP-FPM** | Process running, socket/port responding |
| **MariaDB** | Connection, query execution, thread count |
| **WordPress** | Site response, wp-cron, REST API |

## Quick start

```bash
# Build
make build

# Set sensitive config via environment variables
export MARIADB_DSN="monitor:yourpassword@tcp(127.0.0.1:3306)/mysql"

# Edit config
cp config.yaml /opt/serverstat/config.yaml
vi /opt/serverstat/config.yaml

# Run directly
./serverstat -config config.yaml

# Or install as systemd service
make install
sudo systemctl enable --now serverstat
```

## Endpoints

| Endpoint | Purpose |
|----------|---------|
| `GET /` | Web dashboard (auto-refreshes every 5s) |
| `GET /api/status` | Full JSON status of all components |
| `GET /api/i18n` | Translation strings for current language |
| `GET /healthz` | LB health check (200 = healthy, 503 = unhealthy) |

## Configuration

Edit `config.yaml` to:
- Enable/disable individual checkers
- Set disk/memory warning thresholds
- Configure MariaDB DSN
- Set WordPress URL
- Configure alerts (log, webhook, email)
- Choose language (`en` or `ko`)

### Environment variables

Sensitive values support `${VAR}` expansion in config.yaml:

```yaml
mariadb:
  dsn: "${MARIADB_DSN}"
alerts:
  webhook:
    url: "${WEBHOOK_URL}"
  email:
    username: "${SMTP_USERNAME}"
    password: "${SMTP_PASSWORD}"
```

## Internationalization (i18n)

Server-Stat supports multiple languages. Set `language` in `config.yaml`:

```yaml
language: "ko"  # Korean
```

### Supported languages

| Code | Language |
|------|----------|
| `en` | English (default) |
| `ko` | Korean (한국어) |

### Adding a new language

1. Copy `lang/en.yaml` to `lang/<code>.yaml` (e.g., `lang/ja.yaml`)
2. Translate all values in the new file
3. Keep format verbs (`%s`, `%d`, `%.2f`) in their original positions
4. Set `language: "<code>"` in `config.yaml`
5. Rebuild: `make build`

No Go code changes needed. Translation files are embedded at build time.

## Alerts

- **Log**: Always-on structured JSON logging via slog
- **Webhook**: POST JSON to Slack, Discord, PagerDuty, etc.
- **Email**: SMTP-based email alerts (UTF-8 subject encoding for non-ASCII languages)

Alerts fire on status transitions (e.g., OK -> Critical, Critical -> OK).

## GCP Load Balancer integration

Point your GCP health check at `/healthz`. It returns:
- `200` when nginx, PHP-FPM, and MariaDB are healthy
- `503` when any critical check is failing

The critical checks are configurable in `config.yaml`.

## Security

- Security headers on all responses (`X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`)
- SSRF protection: LB IP validated to prevent URL injection
- Email header injection prevention via CRLF sanitization
- XSS protection: all dynamic values escaped in dashboard
- Sensitive config values support environment variable expansion (no plaintext passwords in config)
- MIME-encoded email subjects for UTF-8 safety

## Architecture

Single static binary, zero CGO dependencies. Reads system metrics directly from `/proc` (no shelling out to `df`/`free`). All checkers run concurrently via goroutines. Persistent connection pools for MariaDB and HTTP clients. Only 2 external dependencies: `go-sql-driver/mysql` and `gopkg.in/yaml.v3`.
