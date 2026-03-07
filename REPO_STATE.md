# Repository State

**Date documented:** 2026-03-07
**Module:** `github.com/alkallio/dns-updater`
**Language:** Go 1.24.1
**License:** GNU General Public License v3.0

---

## Purpose

A dynamic DNS updater daemon that periodically checks the host's external IP address and updates Google Cloud DNS records when the IP changes. Intended for use on machines with dynamic public IPs (e.g., home servers) that need to keep DNS records current.

---

## Repository Structure

```
dns-updater/
├── main.go          # All application logic (single-package program)
├── main_test.go     # Unit tests
├── go.mod           # Module definition and direct dependencies
├── go.sum           # Dependency checksums
├── .gitignore       # Go-standard ignore rules
├── LICENSE          # GNU GPLv3
└── README.md        # Usage and configuration guide
```

**No sub-packages.** Everything is in `package main`.

---

## Git History

| Commit    | Message                     |
|-----------|-----------------------------|
| `fed665d` | Add go script and README    |
| `dcb0779` | Initial commit              |

Active branch: `master` / `claude/document-repo-state-jSeV3`

---

## Architecture & Key Types

### Interfaces (for testability)

```go
// IPFetcher — abstracts external IP retrieval
type IPFetcher interface {
    GetExternalIP() (string, error)
}

// DNSUpdater — abstracts GCP DNS operations
type DNSUpdater interface {
    GetCurrentDNSRecordIP(projectID, zoneName, recordName, recordType string) (string, error)
    UpdateDNSRecord(projectID, zoneName, recordName, recordType, ipAddress string, ttl int64) error
}
```

### Concrete Implementations

| Type             | Interface    | Description                                    |
|------------------|--------------|------------------------------------------------|
| `HTTPIPFetcher`  | `IPFetcher`  | Fetches public IP via HTTP from multiple URLs  |
| `GCPDNSUpdater`  | `DNSUpdater` | Updates records via Google Cloud DNS API       |

### Configuration Types

```go
type DomainConfig struct {
    ZoneName   string  // GCP DNS managed zone name
    RecordName string  // FQDN with trailing dot, e.g. "sub.example.com."
    RecordType string  // "A" or "AAAA"
    TTL        int64   // Record TTL in seconds
}
```

The `domains.json` config file format (from README):
```json
{
  "domains": [
    {
      "zone_name": "example-com-zone",
      "record_name": "sub.example.com.",
      "record_type": "A",
      "ttl": 300
    }
  ]
}
```

---

## Runtime Behaviour

1. **Startup:** Reads a GCP service account key JSON file (path from `os.Args[1]`), extracts `project_id`, authenticates with `google.CredentialsFromJSON`, and creates a `dns.Service` client.
2. **Config load:** Reads `domains.json` from the working directory. If no domains are configured, it saves an empty config and continues running.
3. **Main loop:** Runs an immediate check then ticks every **1 hour** (`ipFetchInterval`).
4. **Per-check (`performCheck`):**
   - Fetches the current external IP via `HTTPIPFetcher`.
   - For each configured domain calls `processRecord`.
5. **Per-record (`processRecord`):**
   - Skips if IP matches the in-memory `lastKnownIPs` cache.
   - Queries the live DNS record via GCP API.
   - Skips update if DNS already matches; updates `lastKnownIPs` cache.
   - Otherwise calls `UpdateDNSRecord` and updates the cache on success.

### IP Fetching

`HTTPIPFetcher` tries URLs in order, stopping at the first success:
1. `https://cloudflare.com/cdn-cgi/trace` — key=value format, parses `ip=` line
2. `https://ipecho.net/plain` — plain-text IP

Responses are validated with `net.ParseIP`.

### DNS Update Mechanism

Uses the GCP Cloud DNS **Changes API** (atomic add+delete):
- Fetches the existing record set to build the deletion list.
- Creates a `Change` with the new record as an addition and the old as a deletion.
- Polls the change status every 5 seconds, with a **5-minute timeout**.

---

## Known Issue: Missing `Config` Implementation

`main.go` calls three functions/methods that are **not defined anywhere** in the repository:

| Symbol | Called at |
|--------|-----------|
| `LoadConfig(configFilePath)` | `main.go:91` |
| `config.GetDomains()` | `main.go:97`, `103`, `148` |
| `config.SaveConfig(configFilePath)` | `main.go:99` |

The `Config` struct and these methods are referenced but never implemented. **The project does not compile in its current state.** This is the primary gap that needs to be addressed before the tool is functional.

---

## Dependencies

### Direct

| Package | Version | Purpose |
|---------|---------|---------|
| `golang.org/x/oauth2` | v0.32.0 | Google OAuth2 authentication |
| `google.golang.org/api` | v0.254.0 | Google Cloud DNS API client |
| `github.com/chmike/domain` | v1.1.0 | Listed in go.mod but not imported in code |

### Notable Indirect

- `cloud.google.com/go/auth` — GCP auth plumbing
- `go.opentelemetry.io/*` — pulled in transitively by the GCP client library

---

## Tests (`main_test.go`)

| Test function | What it covers |
|---------------|----------------|
| `TestIsValidIP` | 9 cases for `isValidIP()` — IPv4, IPv6, invalid formats |
| `TestExtractProjectID` | 5 cases for `extractProjectID()` — valid JSON, missing field, empty field, invalid JSON, empty input |
| `TestParseIPFromKeyValue` | 5 cases for `parseIPFromKeyValue()` — Cloudflare trace format, IPv6, no `ip=` field, empty string, padded whitespace |
| `TestProcessRecord` | 5 cases for the core update logic via `MockDNSUpdater` — no change, DNS already current, update needed, DNS lookup failure, update failure |

Tests use a hand-written `MockDNSUpdater` struct with function-field callbacks. No external test framework is used beyond the standard `testing` package.

**Note:** Tests for `LoadConfig`, `SaveConfig`, `GetDomains`, and any config-layer logic are absent because those are not yet implemented.

---

## What Remains To Be Done

1. **Implement `Config` / `LoadConfig` / `SaveConfig` / `GetDomains`** — the project cannot build without these.
2. **GCP service account setup instructions** — the README notes "TBD" for this step.
3. **`github.com/chmike/domain`** is declared as a direct dependency in `go.mod` but is not imported anywhere; it may be vestigial or intended for future domain-name validation.
4. **No build/run automation** — no `Makefile`, no `Dockerfile`, no systemd unit file for running as a daemon.
5. **No CI configuration** (no GitHub Actions or similar).
