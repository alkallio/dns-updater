package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/dns/v1"
	"google.golang.org/api/option"
)

const (
	defaultIPFetchInterval = 5 * time.Minute
	defaultConfigFilePath  = "domains.json"

	// defaultIPFetchTimeout bounds a single request to an IP reporting service.
	// Without it a half-open connection stalls the check loop indefinitely.
	defaultIPFetchTimeout = 15 * time.Second
)

// serviceAccount struct to unmarshal the project_id from the service account key
type serviceAccount struct {
	ProjectID string `json:"project_id"`
}

// Provider identifiers used by the "provider" field in domains.json.
const (
	ProviderGCP        = "gcp"
	ProviderCloudflare = "cloudflare"
)

// DomainConfig holds configuration for a DNS record
type DomainConfig struct {
	Provider   string `json:"provider"`    // "gcp" or "cloudflare"
	ZoneName   string `json:"zone_name"`   // GCP managed zone name, or Cloudflare zone ID
	RecordName string `json:"record_name"` // FQDN of the record, e.g., "sub.example.com."
	RecordType string `json:"record_type"` // e.g., "A", "AAAA"
	TTL        int64  `json:"ttl"`         // Time-to-live, used by gcp; cloudflare keeps the zone's value
}

// Key returns a unique identifier for this record, used to track the last
// known IP per record. Provider and zone are included because the same record
// name can exist in more than one provider or zone.
func (d DomainConfig) Key() string {
	return strings.Join([]string{d.Provider, d.ZoneName, d.RecordName, d.RecordType}, "|")
}

// IPFetcher interface for fetching external IP addresses. recordType selects
// the address family, so an A record never receives an IPv6 address.
type IPFetcher interface {
	GetExternalIP(recordType string) (string, error)
}

// DNSUpdater interface for DNS operations. Provider credentials and any
// account-level identifiers (such as the GCP project ID) belong to the
// implementation, not to these signatures.
type DNSUpdater interface {
	GetCurrentDNSRecordIP(rec DomainConfig) (string, error)
	UpdateDNSRecord(rec DomainConfig, ipAddress string) error
}

// HTTPIPFetcher implements IPFetcher using HTTP requests. It holds one client
// per address family, each pinned to that family, so the address a source
// reports is the address this site egresses with over that protocol.
type HTTPIPFetcher struct {
	URLs    []string
	Timeout time.Duration

	mu      sync.Mutex
	clients map[string]*http.Client
}

// GCPDNSUpdater implements DNSUpdater using GCP DNS API
type GCPDNSUpdater struct {
	Service   *dns.Service
	ProjectID string
}

func main() {
	configPath := flag.String("config", defaultConfigFilePath, "path to the domains configuration file")
	interval := flag.Duration("interval", defaultIPFetchInterval, "how often to check the external IP")
	flag.Parse()

	// Load configuration first: which provider clients we need depends on it.
	config, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	domainConfigs := config.GetDomains()
	if len(domainConfigs) == 0 {
		log.Printf("No domains configured in %s. Please add domain configurations to begin DNS updates.", *configPath)
		if err := config.SaveConfig(*configPath); err != nil {
			log.Printf("Warning: Failed to save configuration file: %v", err)
		}
	} else {
		log.Printf("Loaded %d domain(s) from configuration file", len(domainConfigs))
	}

	updaters, err := buildUpdaters(context.Background(), domainConfigs, flag.Args())
	if err != nil {
		log.Fatalf("Failed to initialize DNS providers: %v", err)
	}

	ipFetcher := &HTTPIPFetcher{
		URLs: []string{
			"https://cloudflare.com/cdn-cgi/trace",
			"https://ipecho.net/plain",
		},
	}

	lastKnownIPs := make(map[string]string)

	// Main DNS update loop
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	log.Printf("Starting DNS monitoring loop. Checking every %v", *interval)

	// Do first check immediately
	performCheck(config, ipFetcher, updaters, lastKnownIPs, *interval)

	for range ticker.C {
		performCheck(config, ipFetcher, updaters, lastKnownIPs, *interval)
	}
}

// buildUpdaters creates one DNSUpdater per provider referenced by the
// configuration. A provider that no record uses is never initialized, so a
// Cloudflare-only setup needs no GCP service account key and vice versa.
func buildUpdaters(ctx context.Context, domains []DomainConfig, args []string) (map[string]DNSUpdater, error) {
	needed := make(map[string]bool)
	for _, d := range domains {
		provider, err := providerFor(d)
		if err != nil {
			return nil, fmt.Errorf("record %s: %w", d.RecordName, err)
		}
		needed[provider] = true
	}

	updaters := make(map[string]DNSUpdater)

	if needed[ProviderGCP] {
		if len(args) < 1 {
			return nil, fmt.Errorf("configuration contains %q records; usage: %s <path_to_service_account_key.json>", ProviderGCP, os.Args[0])
		}
		updater, err := newGCPDNSUpdater(ctx, args[0])
		if err != nil {
			return nil, err
		}
		updaters[ProviderGCP] = updater
	}

	if needed[ProviderCloudflare] {
		updater, err := NewCloudflareDNSUpdater(os.Getenv(cloudflareTokenEnv))
		if err != nil {
			return nil, err
		}
		updaters[ProviderCloudflare] = updater
		log.Println("Cloudflare DNS client ready")
	}

	return updaters, nil
}

// newGCPDNSUpdater authenticates with the given service account key file.
func newGCPDNSUpdater(ctx context.Context, saKeyPath string) (*GCPDNSUpdater, error) {
	saKeyBytes, err := os.ReadFile(saKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read service account key file: %w", err)
	}

	projectID, err := extractProjectID(saKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to extract project ID: %w", err)
	}

	creds, err := google.CredentialsFromJSON(ctx, saKeyBytes, dns.NdevClouddnsReadwriteScope)
	if err != nil {
		return nil, fmt.Errorf("failed to create credentials from service account key: %w", err)
	}

	dnsService, err := dns.NewService(ctx, option.WithCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("failed to create DNS service client: %w", err)
	}

	log.Printf("GCP DNS client ready for project %s", projectID)
	return &GCPDNSUpdater{Service: dnsService, ProjectID: projectID}, nil
}

// providerFor returns the provider name to use for a record. It is the single
// place that decides how a missing or unknown "provider" field is treated, so
// that buildUpdaters and resolveUpdater can never disagree.
//
// The policy is strict: every record names a supported provider, or startup
// fails. A typo becomes an error instead of a silently skipped record.
func providerFor(rec DomainConfig) (string, error) {
	switch rec.Provider {
	case ProviderGCP, ProviderCloudflare:
		return rec.Provider, nil
	case "":
		return "", fmt.Errorf("missing %q field, expected %q or %q", "provider", ProviderGCP, ProviderCloudflare)
	default:
		return "", fmt.Errorf("unknown provider %q, expected %q or %q", rec.Provider, ProviderGCP, ProviderCloudflare)
	}
}

// resolveUpdater picks the DNSUpdater for a record.
func resolveUpdater(rec DomainConfig, updaters map[string]DNSUpdater) (DNSUpdater, error) {
	provider, err := providerFor(rec)
	if err != nil {
		return nil, err
	}
	updater, ok := updaters[provider]
	if !ok {
		return nil, fmt.Errorf("no client initialized for provider %q", provider)
	}
	return updater, nil
}

// performCheck performs a single DNS update check. The external IP is fetched
// once per address family and reused across every record of that family.
func performCheck(config *Config, ipFetcher IPFetcher, updaters map[string]DNSUpdater, lastKnownIPs map[string]string, interval time.Duration) {
	log.Println("Starting DNS check cycle...")

	domainConfigs := config.GetDomains()
	if len(domainConfigs) == 0 {
		log.Println("No domains configured. Waiting for next check...")
		log.Printf("DNS check cycle completed. Next check in %v", interval)
		return
	}

	// Cache one address per record type for the duration of this cycle.
	ipsByType := make(map[string]string)

	for _, domainConfig := range domainConfigs {
		updater, err := resolveUpdater(domainConfig, updaters)
		if err != nil {
			log.Printf("Skipping %s: %v", domainConfig.RecordName, err)
			continue
		}

		currentIP, ok := ipsByType[domainConfig.RecordType]
		if !ok {
			currentIP, err = ipFetcher.GetExternalIP(domainConfig.RecordType)
			if err != nil {
				log.Printf("Error fetching external IP for %s: %v", domainConfig.RecordName, err)
				continue
			}
			ipsByType[domainConfig.RecordType] = currentIP
			log.Printf("Current external address for %s records: %s", domainConfig.RecordType, currentIP)
		}

		processRecord(domainConfig, currentIP, lastKnownIPs, updater)
	}

	log.Printf("DNS check cycle completed. Next check in %v", interval)
}

// extractProjectID extracts the project ID from service account JSON
func extractProjectID(saKeyBytes []byte) (string, error) {
	var saConf serviceAccount
	if err := json.Unmarshal(saKeyBytes, &saConf); err != nil {
		return "", fmt.Errorf("failed to parse service account key: %w", err)
	}
	if saConf.ProjectID == "" {
		return "", fmt.Errorf("project_id not found in service account key")
	}
	return saConf.ProjectID, nil
}

// processRecord handles the DNS update logic for a single domain configuration
// This function is extracted for testability and contains the core business logic
func processRecord(config DomainConfig, currentIP string, lastKnownIPs map[string]string, dnsUpdater DNSUpdater) {
	log.Printf("Processing record: %s (Provider: %s, Zone: %s, Type: %s)", config.RecordName, config.Provider, config.ZoneName, config.RecordType)
	key := config.Key()
	lastKnownIP := lastKnownIPs[key]

	if currentIP == lastKnownIP {
		log.Printf("IP address (%s) for %s has not changed. No update needed.", currentIP, config.RecordName)
		return
	}

	log.Printf("External IP (%s) differs from last known IP ('%s') for %s. Checking DNS.", currentIP, lastKnownIP, config.RecordName)

	currentDNSRecordIP, err := dnsUpdater.GetCurrentDNSRecordIP(config)
	if err != nil {
		log.Printf("Warning: Could not get current DNS record IP for %s: %v. Proceeding with update attempt.", config.RecordName, err)
	} else {
		log.Printf("Current DNS %s record IP for %s: %s", config.RecordType, config.RecordName, currentDNSRecordIP)
		if currentIP == currentDNSRecordIP {
			log.Printf("External IP (%s) matches current DNS record IP for %s. No update needed.", currentIP, config.RecordName)
			lastKnownIPs[key] = currentIP
			return
		}
	}

	log.Printf("Attempting DNS update for %s to IP %s.", config.RecordName, currentIP)
	err = dnsUpdater.UpdateDNSRecord(config, currentIP)
	if err != nil {
		log.Printf("Error updating DNS record for %s: %v", config.RecordName, err)
		return
	}
	log.Printf("Successfully updated DNS record for %s to %s", config.RecordName, currentIP)
	lastKnownIPs[key] = currentIP
}

// networkForRecordType maps a DNS record type to the TCP network to dial, so
// that the address returned belongs to the family the record can hold.
func networkForRecordType(recordType string) (string, error) {
	switch recordType {
	case "A":
		return "tcp4", nil
	case "AAAA":
		return "tcp6", nil
	default:
		return "", fmt.Errorf("record type %q does not hold an IP address", recordType)
	}
}

// matchesRecordType reports whether ip belongs to the family recordType holds.
func matchesRecordType(ip, recordType string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	if recordType == "A" {
		return parsed.To4() != nil
	}
	return parsed.To4() == nil
}

// clientFor returns the HTTP client pinned to the given network, creating it on
// first use. Pinning the dial network is what makes a dual-stack host report
// its IPv4 address for an A record instead of whichever family it happens to
// prefer.
func (f *HTTPIPFetcher) clientFor(network string) *http.Client {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.clients == nil {
		f.clients = make(map[string]*http.Client)
	}
	if client, ok := f.clients[network]; ok {
		return client
	}

	timeout := f.Timeout
	if timeout <= 0 {
		timeout = defaultIPFetchTimeout
	}

	dialer := &net.Dialer{Timeout: timeout}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
	f.clients[network] = client
	return client
}

// GetExternalIP fetches the external IP for the given record type, trying each
// configured source in turn.
func (f *HTTPIPFetcher) GetExternalIP(recordType string) (string, error) {
	network, err := networkForRecordType(recordType)
	if err != nil {
		return "", err
	}
	client := f.clientFor(network)

	for _, url := range f.URLs {
		log.Printf("Trying to fetch %s address from: %s", recordType, url)

		ip, err := fetchIPFrom(client, url)
		if err != nil {
			log.Printf("Failed to get IP from %s: %v", url, err)
			continue
		}

		if !matchesRecordType(ip, recordType) {
			log.Printf("Address %s from %s is not valid for a %s record", ip, url, recordType)
			continue
		}
		return ip, nil
	}
	return "", fmt.Errorf("failed to fetch %s address from all sources", recordType)
}

// fetchIPFrom reads one IP address from a single source. It is a separate
// function so the response body closes when this source is done, not when the
// whole fetch loop ends.
func fetchIPFrom(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status code %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	body := string(bodyBytes)

	// Check if this is a key=value format (like Cloudflare trace)
	var ip string
	if strings.Contains(body, "ip=") {
		ip = parseIPFromKeyValue(body)
	} else {
		ip = strings.TrimSpace(body)
	}

	if ip == "" {
		return "", fmt.Errorf("could not extract an IP address from the response")
	}
	if !isValidIP(ip) {
		return "", fmt.Errorf("invalid IP address format: %q", ip)
	}
	return ip, nil
}

// parseIPFromKeyValue extracts the IP address from key=value formatted text (e.g., Cloudflare trace)
func parseIPFromKeyValue(body string) string {
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ip=") {
			return strings.TrimPrefix(line, "ip=")
		}
	}
	return ""
}

// isValidIP validates an IP address string
func isValidIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

// GetCurrentDNSRecordIP fetches the current IP from DNS record
func (g *GCPDNSUpdater) GetCurrentDNSRecordIP(rec DomainConfig) (string, error) {
	zoneName, recordName, recordType := rec.ZoneName, rec.RecordName, rec.RecordType
	listCall := g.Service.ResourceRecordSets.List(g.ProjectID, zoneName).Name(recordName).Type(recordType)
	resp, err := listCall.Do()
	if err != nil {
		return "", fmt.Errorf("failed to list resource record sets for %s: %w", recordName, err)
	}

	for _, rrs := range resp.Rrsets {
		if rrs.Type == recordType && rrs.Name == recordName {
			if len(rrs.Rrdatas) > 0 {
				return rrs.Rrdatas[0], nil
			}
			return "", fmt.Errorf("%s record found for %s but no IP address data", recordType, recordName)
		}
	}
	return "", fmt.Errorf("no %s record found for %s", recordType, recordName)
}

// UpdateDNSRecord updates a DNS record with a new IP address
func (g *GCPDNSUpdater) UpdateDNSRecord(rec DomainConfig, ipAddress string) error {
	zoneName, recordName, recordType, ttl := rec.ZoneName, rec.RecordName, rec.RecordType, rec.TTL
	log.Printf("Attempting to update DNS record: Project=%s, Zone=%s, Name=%s, Type=%s, IP=%s, TTL=%d",
		g.ProjectID, zoneName, recordName, recordType, ipAddress, ttl)

	// Get existing records to remove the old record if it exists
	currentRecordSet, err := g.Service.ResourceRecordSets.List(g.ProjectID, zoneName).Name(recordName).Type(recordType).Do()
	var deletions []*dns.ResourceRecordSet

	if err == nil && len(currentRecordSet.Rrsets) > 0 {
		for _, rrs := range currentRecordSet.Rrsets {
			if rrs.Name == recordName && rrs.Type == recordType {
				log.Printf("Found existing record to delete: Name=%s, Type=%s, Rrdatas=%v, Ttl=%d", rrs.Name, rrs.Type, rrs.Rrdatas, rrs.Ttl)
				deletions = append(deletions, rrs)
			}
		}
	} else if err != nil {
		log.Printf("Could not list existing record sets for deletion (may not exist yet): %v", err)
	}

	addition := &dns.ResourceRecordSet{
		Name:    recordName,
		Type:    recordType,
		Ttl:     ttl,
		Rrdatas: []string{ipAddress},
	}

	change := &dns.Change{
		Additions: []*dns.ResourceRecordSet{addition},
		Deletions: deletions,
	}

	if len(change.Deletions) == 0 {
		change.Deletions = nil
	} else {
		log.Printf("Preparing to delete %d record set(s).", len(change.Deletions))
	}
	log.Printf("Preparing to add 1 record set for %s with IP %s.", addition.Name, ipAddress)

	changesCreateCall := g.Service.Changes.Create(g.ProjectID, zoneName, change)
	resp, err := changesCreateCall.Do()
	if err != nil {
		return fmt.Errorf("failed to execute DNS change: %w", err)
	}

	// Wait for the change to complete with timeout
	timeout := time.After(5 * time.Minute) // 5 minute timeout
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for resp.Status == "pending" {
		select {
		case <-timeout:
			return fmt.Errorf("DNS change %s timed out after 5 minutes", resp.Id)
		case <-ticker.C:
			log.Printf("Waiting for DNS change to complete (ID: %s)... Current status: %s", resp.Id, resp.Status)
			changeID := resp.Id
			updated, err := g.Service.Changes.Get(g.ProjectID, zoneName, changeID).Do()
			if err != nil {
				// updated is nil here, so report the ID captured before the call.
				return fmt.Errorf("failed to get status of DNS change %s: %w", changeID, err)
			}
			resp = updated
		}
	}

	if resp.Status == "done" {
		log.Printf("DNS change %s completed successfully.", resp.Id)
		return nil
	}

	return fmt.Errorf("DNS change %s finished with status: %s", resp.Id, resp.Status)
}
