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
| `GET /healthz` | LB health check (200 = healthy, 503 = unhealthy) |

## Configuration

Edit `config.yaml` to:
- Enable/disable individual checkers
- Set disk/memory warning thresholds
- Configure MariaDB DSN
- Set WordPress URL
- Configure alerts (log, webhook, email)

## Alerts

- **Log**: Always-on structured JSON logging via slog
- **Webhook**: POST JSON to Slack, Discord, PagerDuty, etc.
- **Email**: SMTP-based email alerts

Alerts fire on status transitions (e.g., OK -> Critical, Critical -> OK).

## GCP Load Balancer integration

Point your GCP health check at `/healthz`. It returns:
- `200` when nginx, PHP-FPM, and MariaDB are healthy
- `503` when any critical check is failing

The critical checks are configurable in `config.yaml`.

## Architecture

Single static binary, zero CGO dependencies. Reads system metrics directly from `/proc` (no shelling out to `df`/`free`). All checkers run concurrently via goroutines. Only 2 external dependencies: `go-sql-driver/mysql` and `gopkg.in/yaml.v3`.
