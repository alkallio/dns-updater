# DNS Record Updater

Checks your external IP address periodically and updates Google Cloud DNS records when the IP changes.

## Configuration

The tool requires a configuration file `domains.json` to specify the DNS records to update. The format is as follows:

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

## Usage

1. Create a service account in Google Cloud with the appropriate permissions. _[TBD]_
2. Download the service account key JSON file.
3. Create the `domains.json` configuration file.
4. Run the tool with the command:

```bash
go run main.go <path_to_service_account_key.json>
```