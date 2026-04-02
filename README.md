# Server-Stat

A lightweight server monitoring tool built in Go. Auto-detects services, works across major Linux distributions, and supports nginx and Apache.

## Supported Linux distributions

| Distro family | Tested on |
|---------------|-----------|
| **Debian** | Debian, Ubuntu, Linux Mint |
| **RHEL** | RHEL, CentOS, Rocky, AlmaLinux, Fedora |
| **Arch** | Arch Linux, Manjaro |
| **Alpine** | Alpine Linux |
| **SUSE** | openSUSE, SLES |

## What it monitors

| Component | Checks |
|-----------|--------|
| **GCP Load Balancer** | Metadata server, LB path verification |
| **Linux OS** | Disk usage, memory, load average |
| **Firewall** | Port accessibility (80, 443, 3306) |
| **Nginx** | Process running, config valid, HTTP response |
| **Apache** | Process running, config valid, HTTP response |
| **PHP-FPM** | Process running, socket/port responding |
| **MariaDB** | Connection, query execution, thread count |
| **WordPress** | Site response, wp-cron, REST API |
| **Log watcher** | 26 known error patterns with mitigation advice |

## Quick start

```bash
# One-line install (builds and installs)
make install

# Or step by step:
make build
sudo bash install.sh
```

The installer auto-detects your distro, HTTP server (nginx/Apache), init system (systemd/OpenRC), and generates a config file.

## Uninstall

```bash
# Interactive (asks for confirmation)
sudo bash uninstall.sh

# Non-interactive
sudo bash uninstall.sh -y

# Or via make
make uninstall
```

Removes binary, config, and service files cleanly.

## Auto-detection

Server-Stat automatically detects:

| What | How |
|------|-----|
| **Distro** | Reads `/etc/os-release` |
| **HTTP server** | Checks for `nginx` or `apache2`/`httpd` in PATH |
| **PHP-FPM socket** | Scans common paths across distros |
| **Nginx/Apache PID** | Scans common paths across distros |
| **Log files** | Checks which log files exist on the system |

Set `http_server.type: "auto"` in config (default) for automatic detection, or explicitly set `"nginx"` or `"apache"`.

## Endpoints

| Endpoint | Purpose |
|----------|---------|
| `GET /` | Web dashboard (auto-refreshes every 5s) |
| `GET /api/status` | Full JSON status of all components |
| `GET /api/logs` | Recent log warnings with mitigation advice |
| `GET /api/i18n` | Translation strings for current language |
| `GET /healthz` | LB health check (200 = healthy, 503 = unhealthy) |

## Configuration

Edit `config.yaml` to enable/disable checkers, set thresholds, and configure alerts. All paths auto-detect by default.

### Environment variables

Sensitive values support `${VAR}` expansion:

```yaml
mariadb:
  dsn: "${MARIADB_DSN}"
alerts:
  webhook:
    url: "${WEBHOOK_URL}"
```

## Internationalization (i18n)

| Code | Language |
|------|----------|
| `en` | English (default) |
| `ko` | Korean (한국어) |

Adding a new language: copy `lang/en.yaml` to `lang/<code>.yaml`, translate, rebuild. No code changes needed.

## Log watcher

Tails service log files and matches 26 known error patterns:

| Service | Example patterns |
|---------|-----------------|
| **Nginx** | worker_connections, upstream timeout, too many open files |
| **Apache** | MaxRequestWorkers, SSL errors, request timeout |
| **PHP-FPM** | memory limit, max_children, fatal errors |
| **MariaDB** | too many connections, deadlocks, table full |
| **System** | OOM killer, filesystem errors, conntrack table full |

Each match shows the error title and actionable mitigation steps (in the configured language).

## Resource usage

- **Memory**: ~11 MB RSS at runtime (hard-capped at 80 MB)
- **Binary**: Single static binary, zero CGO, 2 external deps
- **CPU**: Negligible (checks run every 30s, reads `/proc` directly)

## Architecture

Single static binary. Reads `/proc` directly (no shelling out to `df`/`free`). All checkers run concurrently via goroutines. Persistent connection pools for MariaDB and HTTP clients. Log watcher uses ring buffer with bounded memory.
