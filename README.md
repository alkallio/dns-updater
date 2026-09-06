# DNS Record Updater

Checks your external IP address periodically and updates DNS records when the IP changes.
Supports Google Cloud DNS and Cloudflare, in the same configuration file.

## Configuration

The tool reads `domains.json` from the working directory. Each entry names its provider:

```json
{
  "domains": [
    {
      "provider": "gcp",
      "zone_name": "example-com-zone",
      "record_name": "sub.example.com.",
      "record_type": "A",
      "ttl": 300
    },
    {
      "provider": "cloudflare",
      "zone_name": "023e105f4ecef8ad9ca31a8372d0c353",
      "record_name": "sub.example.org.",
      "record_type": "A",
      "ttl": 300
    }
  ]
}
```

Field meanings are in the `DomainConfig` struct in `main.go`. Two things differ per provider:

- `zone_name` holds the managed zone name for `gcp`, and the **zone ID** for `cloudflare`
  (Cloudflare dashboard, zone Overview page, right sidebar).
- `ttl` is used by `gcp`. On `cloudflare` the record keeps the TTL the zone already has.

The trailing dot in `record_name` is required by Google Cloud DNS. The Cloudflare client
strips it, so one record name format works for both.

`provider` is mandatory on every entry. A missing or unrecognized value stops the tool at
startup rather than skipping the record, so a typo cannot go unnoticed.

`record_type` must be `A` or `AAAA`. The address family follows from it: the external IP for
an `A` record is fetched over IPv4 and for an `AAAA` record over IPv6, so a dual-stack host
publishes the right address for each.

## What the updater owns

On `cloudflare` the updater changes **only the value** of a record:

- It sends a content-only `PATCH`. TTL and the proxy setting stay as the zone has them, so a
  system that manages the zone (Terraform, for instance) keeps ownership of those fields.
- It does not create records. A missing record is reported as an error every cycle, because
  an absent record is a fault to report and not a condition to repair.

On `gcp` the updater writes a full record set and creates the record if it is absent, which
is inherent to how the Cloud DNS change API is used.

> **Migrating an older `domains.json`:** earlier versions wrote PascalCase keys (`"ZoneName"`)
> which never matched the documented snake_case format. Rename the keys as shown above and add
> `provider` to each entry, or the file loads as empty values.

## Credentials

Only the providers your configuration actually references are initialized.

| Provider | Credential | Passed as |
| --- | --- | --- |
| `gcp` | Service account key JSON | First command line argument |
| `cloudflare` | API token | `CLOUDFLARE_API_TOKEN` environment variable |

The Cloudflare token needs the **Zone / DNS / Edit** permission on the zones you update.
Create it at <https://dash.cloudflare.com/profile/api-tokens>.

## Usage

Both providers:

```bash
export CLOUDFLARE_API_TOKEN=...
go run . <path_to_service_account_key.json>
```

Cloudflare only — no service account key needed:

```bash
export CLOUDFLARE_API_TOKEN=...
go run .
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-config` | `domains.json` | Path to the configuration file |
| `-interval` | `5m` | How often to check the external IP |
| `-retry-interval` | `30s` | How soon to retry after the external IP could not be read |
| `-version` | | Print the version and exit |

The tool runs until stopped. It reads its configuration once, at startup.

## Build and versions

```bash
make build     # builds ./dns-updater, stamped with the version
make test      # gofmt, go vet, go test
make install   # replaces the binary in the instance directory
```

`make build` takes the version from `git describe`, so a binary always names a release and a
build from uncommitted work is marked `-dirty`. A plain `go build` still identifies itself,
from the VCS data the Go toolchain stamps in.

```bash
./dns-updater -version     # what this binary is
```

A running instance logs its version on the first line at startup, so the journal answers the
same question. Changes by version are in [CHANGELOG.md](CHANGELOG.md).

## Limitations

Each record is treated as holding a single IP. Where a name has several values, only the first
is read and updated.

The configuration is read once at startup. Editing `domains.json` needs a restart.
