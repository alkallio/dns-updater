package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/dns/v1"
	"google.golang.org/api/option"
)

const (
	ipFetchInterval = 1 * time.Hour
	configFilePath  = "domains.json"
)

// serviceAccount struct to unmarshal the project_id from the service account key
type serviceAccount struct {
	ProjectID string `json:"project_id"`
}

// DomainConfig holds configuration for a DNS record
type DomainConfig struct {
	ZoneName   string // GCP DNS Zone name
	RecordName string // FQDN of the record, e.g., "sub.example.com." (note the trailing dot)
	RecordType string // e.g., "A", "AAAA"
	TTL        int64  // Time-to-live for the DNS record in seconds
}

// IPFetcher interface for fetching external IP addresses
type IPFetcher interface {
	GetExternalIP() (string, error)
}

// DNSUpdater interface for DNS operations
type DNSUpdater interface {
	GetCurrentDNSRecordIP(projectID, zoneName, recordName, recordType string) (string, error)
	UpdateDNSRecord(projectID, zoneName, recordName, recordType, ipAddress string, ttl int64) error
}

// HTTPIPFetcher implements IPFetcher using HTTP requests
type HTTPIPFetcher struct {
	URLs []string
}

// GCPDNSUpdater implements DNSUpdater using GCP DNS API
type GCPDNSUpdater struct {
	Service *dns.Service
}

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Usage: %s <path_to_service_account_key.json>", os.Args[0])
	}
	saKeyPath := os.Args[1]

	saKeyBytes, err := os.ReadFile(saKeyPath)
	if err != nil {
		log.Fatalf("Failed to read service account key file: %v", err)
	}

	projectID, err := extractProjectID(saKeyBytes)
	if err != nil {
		log.Fatalf("Failed to extract project ID: %v", err)
	}
	log.Printf("Using Project ID: %s", projectID)

	// Authenticate using the service account
	ctx := context.Background()
	creds, err := google.CredentialsFromJSON(ctx, saKeyBytes, dns.NdevClouddnsReadwriteScope)
	if err != nil {
		log.Fatalf("Failed to create credentials from service account key: %v", err)
	}

	dnsService, err := dns.NewService(ctx, option.WithCredentials(creds))
	if err != nil {
		log.Fatalf("Failed to create DNS service client: %v", err)
	}

	log.Printf("Successfully authenticated and created DNS service client for project %s", projectID)

	// Load configuration from file
	config, err := LoadConfig(configFilePath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// If config file is empty, create it
	if len(config.GetDomains()) == 0 {
		log.Printf("No domains configured in %s. Please add domain configurations to begin DNS updates.", configFilePath)
		if err := config.SaveConfig(configFilePath); err != nil {
			log.Printf("Warning: Failed to save configuration file: %v", err)
		}
	} else {
		log.Printf("Loaded %d domain(s) from configuration file", len(config.GetDomains()))
	}

	ipFetcher := &HTTPIPFetcher{
		URLs: []string{
			"https://cloudflare.com/cdn-cgi/trace",
			"https://ipecho.net/plain",
		},
	}
	dnsUpdater := &GCPDNSUpdater{Service: dnsService}

	lastKnownIPs := make(map[string]string)

	// Main DNS update loop
	ticker := time.NewTicker(ipFetchInterval)
	defer ticker.Stop()

	log.Printf("Starting DNS monitoring loop. Checking every %v", ipFetchInterval)

	// Do first check immediately
	performCheck(projectID, config, ipFetcher, dnsUpdater, lastKnownIPs)

	for range ticker.C {
		performCheck(projectID, config, ipFetcher, dnsUpdater, lastKnownIPs)
	}
}

// performCheck performs a single DNS update check
func performCheck(projectID string, config *Config, ipFetcher IPFetcher, dnsUpdater DNSUpdater, lastKnownIPs map[string]string) {
	log.Println("Starting DNS check cycle...")

	currentIP, err := ipFetcher.GetExternalIP()
	if err != nil {
		log.Printf("Error fetching external IP: %v", err)
		return
	}

	if currentIP == "" {
		log.Println("Could not determine external IP")
		return
	}

	log.Printf("Current external IP: %s", currentIP)

	// Get current domains from config
	domainConfigs := config.GetDomains()
	if len(domainConfigs) == 0 {
		log.Println("No domains configured. Waiting for next check...")
	} else {
		for _, domainConfig := range domainConfigs {
			processRecord(projectID, domainConfig, currentIP, lastKnownIPs, dnsUpdater)
		}
	}

	log.Printf("DNS check cycle completed. Next check in %v", ipFetchInterval)
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
func processRecord(projectID string, config DomainConfig, currentIP string, lastKnownIPs map[string]string, dnsUpdater DNSUpdater) {
	log.Printf("Processing record: %s (Zone: %s, Type: %s)", config.RecordName, config.ZoneName, config.RecordType)
	lastKnownIP := lastKnownIPs[config.RecordName]

	if currentIP == lastKnownIP {
		log.Printf("IP address (%s) for %s has not changed. No update needed.", currentIP, config.RecordName)
		return
	}

	log.Printf("External IP (%s) differs from last known IP ('%s') for %s. Checking DNS.", currentIP, lastKnownIP, config.RecordName)

	currentDNSRecordIP, err := dnsUpdater.GetCurrentDNSRecordIP(projectID, config.ZoneName, config.RecordName, config.RecordType)
	if err != nil {
		log.Printf("Warning: Could not get current DNS record IP for %s: %v. Proceeding with update attempt.", config.RecordName, err)
	} else {
		log.Printf("Current DNS %s record IP for %s: %s", config.RecordType, config.RecordName, currentDNSRecordIP)
		if currentIP == currentDNSRecordIP {
			log.Printf("External IP (%s) matches current DNS record IP for %s. No update needed.", currentIP, config.RecordName)
			lastKnownIPs[config.RecordName] = currentIP
			return
		}
	}

	log.Printf("Attempting DNS update for %s to IP %s.", config.RecordName, currentIP)
	err = dnsUpdater.UpdateDNSRecord(projectID, config.ZoneName, config.RecordName, config.RecordType, currentIP, config.TTL)
	if err != nil {
		log.Printf("Error updating DNS record for %s: %v", config.RecordName, err)
		return
	}
	log.Printf("Successfully updated DNS record for %s to %s", config.RecordName, currentIP)
	lastKnownIPs[config.RecordName] = currentIP
}

// GetExternalIP fetches the external IP from configured URLs
func (f *HTTPIPFetcher) GetExternalIP() (string, error) {
	for _, url := range f.URLs {
		log.Printf("Trying to fetch IP from: %s", url)
		resp, err := http.Get(url)
		if err != nil {
			log.Printf("Failed to get IP from %s: %v", url, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Printf("Failed to get IP from %s: status code %d", url, resp.StatusCode)
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Failed to read response body from %s: %v", url, err)
			continue
		}

		body := string(bodyBytes)

		// Check if this is a key=value format (like Cloudflare trace)
		var ip string
		if strings.Contains(body, "ip=") {
			// Parse key=value format
			ip = parseIPFromKeyValue(body)
		} else {
			// Treat as plain text IP
			ip = strings.TrimSpace(body)
		}

		if ip == "" {
			log.Printf("Could not extract IP from response from %s", url)
			continue
		}

		if !isValidIP(ip) {
			log.Printf("Invalid IP address format received from %s: %s", url, ip)
			continue
		}
		return ip, nil
	}
	return "", fmt.Errorf("failed to fetch IP from all sources")
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
func (g *GCPDNSUpdater) GetCurrentDNSRecordIP(projectID, zoneName, recordName, recordType string) (string, error) {
	listCall := g.Service.ResourceRecordSets.List(projectID, zoneName).Name(recordName).Type(recordType)
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
func (g *GCPDNSUpdater) UpdateDNSRecord(projectID, zoneName, recordName, recordType, ipAddress string, ttl int64) error {
	log.Printf("Attempting to update DNS record: Project=%s, Zone=%s, Name=%s, Type=%s, IP=%s, TTL=%d",
		projectID, zoneName, recordName, recordType, ipAddress, ttl)

	// Get existing records to remove the old record if it exists
	currentRecordSet, err := g.Service.ResourceRecordSets.List(projectID, zoneName).Name(recordName).Type(recordType).Do()
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

	changesCreateCall := g.Service.Changes.Create(projectID, zoneName, change)
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
			resp, err = g.Service.Changes.Get(projectID, zoneName, resp.Id).Do()
			if err != nil {
				return fmt.Errorf("failed to get status of DNS change %s: %w", resp.Id, err)
			}
		}
	}

	if resp.Status == "done" {
		log.Printf("DNS change %s completed successfully.", resp.Id)
		return nil
	}

	return fmt.Errorf("DNS change %s finished with status: %s", resp.Id, resp.Status)
}
