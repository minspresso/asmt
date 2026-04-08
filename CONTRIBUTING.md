# Contributing to ASMT

Thanks for your interest in ASMT. This document covers the practical things
you need to know to land a change.

## Before you start

- For anything larger than a typo or a small bug fix, please open an issue
  first to discuss the approach. ASMT has a strong design philosophy
  (see [LEARNINGS.md](LEARNINGS.md)) and a change that conflicts with it
  may be hard to merge even if the code is clean.
- For security issues, **do not** open a public issue.
  See [SECURITY.md](SECURITY.md).

## Design principles

ASMT holds itself to the rules in
[LEARNINGS.md → "Design principles"](LEARNINGS.md#design-principles-the-rules-id-ship-today).
The most load-bearing ones for contributors are:

1. **Trust the OS more than yourself.** The systemd journal is always
   authoritative; our buffer is a cache.
2. **Never invent data.** Missing is information; faking is a lie.
3. **Bound every subprocess.** Time, output size, line count.
4. **Aggregate on write, not on read.** Memory must be bounded by
   dimensions, not by event rate.
5. **Minimize the auth dependency footprint.** Don't add authentication
   that depends on the thing being monitored.

If your change violates one of these, expect a long review.

## Development setup

Requirements: **Go 1.22 or later** and a Linux host (or a container) for
testing the OS-specific checkers.

```bash
git clone https://github.com/minspresso/asmt.git
cd asmt
make build       # produces ./serverstat
./serverstat -config config.yaml
```

Open `http://localhost:8080` for the dashboard.

## Running the checks CI runs

Every PR runs the following on `ubuntu-latest` with the latest stable
Go (`go-version: "stable"` in `actions/setup-go`). Run them locally
before pushing:

```bash
go mod verify
go build ./...
go build -race ./...
go vet ./...
go test -race -short ./...
golangci-lint run --timeout=5m
govulncheck ./...     # advisory in CI, gating in the nightly job
```

`build`, `vet`, `test`, and `golangci-lint` are **hard gates**: a
failure blocks merge. `govulncheck` is **advisory in the per-push
workflow** (`continue-on-error: true`) because it depends on a live
vulnerability database and a brand-new stdlib CVE can flag the build
even when no human touched the repo. The same check runs nightly in
`.github/workflows/vulncheck.yml` as a hard gate so the maintainer
gets notified by email when a real bump is needed.

The lint config is in `.golangci.yml`. The CI workflows are
`.github/workflows/ci.yml` and `.github/workflows/vulncheck.yml`.

## Coding conventions

- **Single flat `package main`.** ASMT is intentionally not split into
  internal packages. The whole tool is about 3,500 lines and the flat
  layout keeps it grep-able. Don't introduce subpackages without
  discussing first.
- **No external runtime dependencies.** A pull request that adds a new
  module to `go.mod` needs a strong justification. The current dependency
  set is `gopkg.in/yaml.v3` and `github.com/go-sql-driver/mysql`. That is
  the bar.
- **No CGO.** Builds use `CGO_ENABLED=0` so the binary stays a single
  static file that runs on any glibc/musl distro.
- **slog, not log.** All logging goes through `log/slog` with structured
  fields, not `log.Printf`.
- **i18n.** User-facing strings live in `lang/en.yaml` and `lang/ko.yaml`.
  Code references them through `tr.T("key")`. Don't hardcode English in
  Go source for anything that reaches the dashboard or alert messages.
- **Translations.** If you add an English string, add the Korean
  equivalent in the same PR. Machine translation is fine as a first
  pass. A native speaker will review it.
- **Comments explain *why*, not *what*.** The code already says what.
  Comments earn their keep by recording the reason a decision was made,
  especially anything counterintuitive.

## Adding a new checker

1. Create `check_<name>.go` next to the existing checkers. Copy the
   shape of `check_redis.go` or `check_postgres.go`.
2. Implement the `Checker` interface from `checker.go`.
3. Register it in `main.go` behind a config flag (`cfg.Checks.<Name>.Enabled`).
4. Add config in `config.go` and a default block in
   `scripts/install.sh`'s generated config.
5. Add a row to the "What it monitors" table in `README.md`.
6. Add unit tests if there is parsing logic worth testing. Avoid tests
   that require a live external service unless they degrade gracefully.

## Adding a new log pattern

Patterns live in `logwatch.go` (`DefaultLogPatterns()`). Each pattern needs
a stable ID, a substring or regex matcher, a title, and a fix string. Add
both English and Korean translations in the `lang/` files.

## Commit messages

Short imperative subject line, optional body explaining the *why*. Example:

```
checker: bound nginx -t output to 16 KiB

Without a cap, a misbehaving config could produce megabytes of warnings
on a single check tick and balloon the buffer. Matches the principle in
LEARNINGS.md ("bound every subprocess").
```

We don't enforce Conventional Commits but we appreciate clear subjects.

## Pull request checklist

- [ ] `go test -race -short ./...` passes locally
- [ ] `go vet ./...` is clean
- [ ] `golangci-lint run` is clean
- [ ] Translations updated for both `en` and `ko` if user-facing
- [ ] README updated if behavior, config, or installer changed
- [ ] No new dependencies in `go.mod` (or strong justification in the PR)
- [ ] No secrets, personal data, or hostnames in the diff

## License of contributions

By submitting a contribution you agree that it will be released under the
project's license, **AGPL-3.0-or-later**. Don't submit code you can't
relicense under AGPL.
