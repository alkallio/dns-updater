# Changelog

Notable changes to this project. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

A built binary reports its own version with `dns-updater -version`, and logs it at startup.

## [Unreleased]

### Added

- `-retry-interval` (default 30s). A cycle that cannot read the external IP now comes back
  after this interval instead of waiting out the full `-interval`. A failed read means the
  network is down and the records are stale until it returns. A failed *write* keeps the
  normal interval, because it can be a permanent fault such as an absent record or a
  read-only token, and retrying that every few seconds would not fix it.
- `-version`, and a version line at startup, so a running instance names its own build.
- `Makefile` with `build`, `test` and `install` targets. `install` replaces the instance
  binary with `mv`, which is atomic, because the old binary may be running.

## [1.0.0] - 2026-09-06

First version in service. It owns `mail.alkallio.com` on the Fedora host and was proved
against a real ISP lease change the same day: the address changed while the router was down,
and the record followed with no human involved.

### Added

- Cloudflare as a DNS provider, alongside Google Cloud DNS. Each record names its provider in
  `domains.json`, so one process serves both. Only the providers a configuration references
  are initialized, so a Cloudflare-only setup needs no service account key.
- `-config` and `-interval` flags, so a deployment does not depend on its working directory.

### Changed

- Cloudflare writes are scoped to the value of a record. The updater sends a content-only
  `PATCH` and never creates a record: the existence, the TTL and the proxy setting belong to
  whatever manages the zone, and an absent record is reported rather than repaired.
- The default interval is 5 minutes, not 1 hour. An hour of staleness on a 300s record is a
  long silent outage.
- `DNSUpdater` takes a `DomainConfig` instead of loose strings, and the GCP project ID moved
  onto the updater that needs it.

### Fixed

- A nil dereference in the GCP change poll took the whole process down on any transient API
  error.
- The external IP fetch had no timeout, so a half-open connection stalled the check loop
  permanently while the service still read as healthy.
- The address family was never matched to the record type, so on a dual-stack host an IPv6
  address could be written into an `A` record. Requests are now pinned to the family the
  record holds.
- `domains.json` keys never parsed. `DomainConfig` had no JSON tags, so the documented
  snake_case keys silently loaded as empty values. **Configuration files written before this
  version must be migrated**; see the README.
- Records were tracked by name alone, so the same name in two providers collided.

[Unreleased]: https://github.com/alkallio/dns-updater/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/alkallio/dns-updater/releases/tag/v1.0.0
