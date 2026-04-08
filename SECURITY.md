# Security Policy

## Supported versions

asmt is pre-1.0. Only the latest tagged release on `main` receives security fixes.

| Version | Supported |
|---------|-----------|
| latest release | ✅ |
| anything older | ❌ |

## Reporting a vulnerability

**Please do not open a public GitHub issue for security problems.**

Instead, report privately via one of the following:

1. **GitHub private vulnerability report** (preferred): use the
   [Report a vulnerability](https://github.com/minspresso/asmt/security/advisories/new)
   button on the repository's Security tab.
2. **Email**: open a public GitHub issue titled `request security contact`
   asking for an email address; the maintainer will reply with one. (We avoid
   publishing a static address here to limit scraping.)

When reporting, please include:

- A description of the vulnerability and the impact you believe it has.
- Steps to reproduce, ideally with a minimal config or curl invocation.
- The version (`serverstat -version` or the release tag) and OS/distro.
- Whether you are willing to be credited in the eventual advisory.

## What to expect

- **Acknowledgement**: within 5 business days.
- **Initial assessment**: within 10 business days, including a severity
  estimate and a tentative timeline.
- **Fix and disclosure**: coordinated. We will not publish details until a
  patched release is available, and we will credit reporters who wish to be
  named.

## Scope

In scope:

- The `serverstat` binary built from this repository.
- The installer scripts (`scripts/get.sh`, `scripts/install.sh`, `scripts/uninstall.sh`).
- The bundled web dashboard (`web/dashboard.html`).
- Default configurations shipped with the installer.

Out of scope:

- Vulnerabilities in third-party software asmt monitors (nginx, MariaDB,
  Redis, etc.) — please report those upstream.
- Self-inflicted misconfigurations such as exposing the dashboard on a public
  interface without an authenticating reverse proxy. asmt logs a loud warning
  in this case but cannot prevent it.
- Issues that require an attacker to already have root on the host running
  asmt.

## Hardening notes for operators

- The dashboard binds to `127.0.0.1:8080` by default and has **no built-in
  authentication**. If you bind to a non-loopback interface, you **must** put
  an authenticating reverse proxy in front of it (HTTP Basic Auth at the
  proxy is the recommended minimum — see
  [LEARNINGS.md → "the auth lesson"](LEARNINGS.md#the-auth-lesson-dont-make-the-tool-depend-on-the-thing-it-monitors)).
- Keep secrets in `/opt/serverstat/env` (chmod 600), never in `config.yaml`.
- The one-line installer verifies the SHA-256 checksum of the release
  archive before installing. Do not bypass this check.
- Run `govulncheck ./...` and `golangci-lint run` if you build from source —
  CI runs both on every commit.
