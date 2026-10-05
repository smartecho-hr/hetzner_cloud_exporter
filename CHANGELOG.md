# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and versions follow
[Semantic Versioning](https://semver.org/). Metric, label and alert renames are listed under
*Breaking*.

## [Unreleased]

## [v0.4.3] - 2026-10-05

First public release.

### Added

- Collectors for servers, Load Balancers (with target health and its reason), certificates,
  volumes, floating and primary IPs, snapshots and backups, Storage Boxes, SSH keys and prices,
  each one switchable and with its own fetch interval.
- Background polling: scrapes never call the Hetzner API; automatic backoff on HTTP 429 and
  when the API budget is low; the Storage Box API's separate budget is handled on its own.
- Per-resource net monthly prices in the account's currency, and cost and unused-resource alerts.
- Every option as a command line flag, an environment variable and a `config.yaml` key; the API
  token also from a file (Docker/Kubernetes secrets).
- TLS and basic auth through the Prometheus web config file.
- `/metrics`, `/healthz`, `/ready`, `/version`; a `healthcheck` subcommand for container health
  checks.
- Five Grafana dashboards (Load Balancers, servers, costs, inventory, exporter health) with a
  data source and a job selector, and 34 Prometheus alerts; promtool unit tests for every alert
  and every dashboard query.
- Docker image `ghcr.io/smartecho-hr/hetzner-cloud-exporter` (amd64, arm64; non-root, read-only
  binary, health check) and release archives for Linux, macOS (`.tar.gz`) and Windows (`.zip`),
  amd64 and arm64, with the dashboards and alerts included, both published by the release workflow; `go install github.com/smartecho-hr/hetzner_cloud_exporter@latest`.

[Unreleased]: https://github.com/smartecho-hr/hetzner_cloud_exporter/compare/v0.4.3...HEAD
[v0.4.3]: https://github.com/smartecho-hr/hetzner_cloud_exporter/releases/tag/v0.4.3
