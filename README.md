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
      "ttl": 300,
      "proxied": false
    }
  ]
}
```

Field meanings are in the `DomainConfig` struct in `main.go`. Two things differ per provider:

- `zone_name` holds the managed zone name for `gcp`, and the **zone ID** for `cloudflare`
  (Cloudflare dashboard, zone Overview page, right sidebar).
- `proxied` applies to `cloudflare` only. A proxied record is forced to Cloudflare's
  automatic TTL; the `ttl` value is ignored.

The trailing dot in `record_name` is required by Google Cloud DNS. The Cloudflare client
strips it, so one record name format works for both.

`provider` is mandatory on every entry. A missing or unrecognized value stops the tool at
startup rather than skipping the record, so a typo cannot go unnoticed.

If a record does not exist in the zone yet, both providers create it.

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

## Limitations

Each record is treated as holding a single IP. Where a name has several values, only the first
is read and updated.
