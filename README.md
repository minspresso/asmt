# Server-Stat

A lightweight server monitoring tool built in Go. Single static binary (~9 MB), zero runtime dependencies, ~11 MB RSS. Auto-detects services and works across major Linux distributions.

## What it does

Server-Stat runs as a background service and continuously checks the health of your server's components. It exposes a live web dashboard and a JSON API so you can see the current state of everything at a glance.

### What it monitors

| Component | Checks performed |
|-----------|-----------------|
| **Linux OS** | Disk usage, memory usage, load average |
| **Nginx** | Process running (PID + signal 0), config valid (`nginx -t`), HTTP probe |
| **Apache** | Process running, config valid (`apachectl -t`), HTTP probe |
| **PHP-FPM** | Process in `/proc`, socket/port responding |
| **MariaDB** | Connection alive, `SELECT 1`, thread count |
| **PostgreSQL** | Connection alive, `SELECT 1`, active connection count |
| **Redis** | PING/PONG over raw TCP, optional AUTH |
| **WordPress** | Site HTTP response, wp-cron endpoint, REST API |
| **Custom HTTP endpoints** | Status code, optional body substring, custom headers |
| **Firewall** | TCP dial to configured ports on localhost |
| **GCP Load Balancer** | Metadata server reachable, LB path probe |
| **Log watcher** | Tails log files, matches 26 known error patterns with mitigation advice |

### Endpoints

| Endpoint | Purpose |
|----------|---------|
| `GET /` | Web dashboard (auto-refreshes every 5 s) |
| `GET /api/status` | Full JSON status of all components |
| `GET /api/logs` | Recent log warnings with mitigation advice |
| `GET /healthz` | Load balancer health check — `200 OK` or `503` |

### Supported Linux distributions

Debian, Ubuntu, Linux Mint, RHEL, CentOS, Rocky Linux, AlmaLinux, Fedora, Arch, Manjaro, Alpine, openSUSE, SLES.

### Alerts

Supports log alerting, webhook (POST JSON), and email (SMTP) on status transitions.

---

## Install from a release (no Go required)

Download a pre-built binary from the [Releases page](../../releases), then run the installer.

```bash
# Replace VERSION and ARCH as needed (amd64 or arm64)
VERSION=v1.0.0
ARCH=amd64

curl -LO https://github.com/your-org/server-stat/releases/download/${VERSION}/serverstat-${VERSION}-linux-${ARCH}.tar.gz
tar xzf serverstat-${VERSION}-linux-${ARCH}.tar.gz
cd serverstat-${VERSION}-linux-${ARCH}

sudo bash scripts/install.sh
```

The installer:
- Detects your distro, HTTP server (nginx/Apache), and init system (systemd/OpenRC)
- Copies the binary to `/opt/serverstat/`
- Generates a starter `config.yaml` based on what it finds
- Installs and registers a systemd or OpenRC service

After installing:

```bash
# Edit the config
sudo nano /opt/serverstat/config.yaml

# Set secrets as environment variables (never hardcode in config)
export MARIADB_DSN='monitor:password@tcp(127.0.0.1:3306)/mysql'

# Start the service (systemd)
sudo systemctl enable --now serverstat

# Or on OpenRC (Alpine)
sudo rc-update add serverstat default
sudo rc-service serverstat start
```

Dashboard is at `http://localhost:8080` (localhost only by default).

---

## Install from source (requires Go 1.21+)

```bash
git clone https://github.com/your-org/server-stat.git
cd server-stat

make build          # produces ./serverstat binary
sudo bash scripts/install.sh
```

Or build and install in one step:

```bash
make install        # builds if needed, then runs install.sh
```

To cross-compile for a remote server:

```bash
make build-amd64    # Linux amd64
make build-arm64    # Linux arm64
```

Then copy the files and install remotely:

```bash
scp serverstat scripts/install.sh scripts/uninstall.sh config.yaml user@server:~/
ssh user@server 'sudo bash install.sh'
```

---

## Uninstall

```bash
# Interactive
sudo bash scripts/uninstall.sh

# Non-interactive
sudo bash scripts/uninstall.sh -y

# Via make
make uninstall
```

Removes the binary, config, and service files from the system.

---

## Configuration

The config file lives at `/opt/serverstat/config.yaml` after install. A reference copy is in `config.yaml` at the root of this repo.

Sensitive values use `${ENV_VAR}` expansion — never hardcode secrets:

```yaml
mariadb:
  dsn: "${MARIADB_DSN}"
redis:
  password: "${REDIS_PASSWORD}"
alerts:
  webhook:
    url: "${WEBHOOK_URL}"
```

Set language to `"en"` (English, default) or `"ko"` (Korean).

---

## Build a distributable archive

```bash
make dist                     # current platform
make dist GOARCH=arm64        # cross-compile for ARM
```

Produces `serverstat-VERSION-linux-ARCH.tar.gz` containing the binary, scripts, config, and README.

---

## Resource usage

| Metric | Value |
|--------|-------|
| Binary size | ~4–5 MB (stripped) |
| RSS at runtime | ~11 MB |
| Memory hard cap | 50 MB |
| CPU | Negligible — checks run every 30 s |

---

## Project layout

```
*.go              — All source (flat package main, ~3100 lines)
lang/             — Translation YAML files (en, ko)
web/              — Dashboard HTML (embedded at build time)
scripts/          — install.sh, uninstall.sh
config.yaml       — Reference configuration
Makefile
```

---

## License

See [LICENSE](LICENSE).
