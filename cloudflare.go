package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	cloudflareAPIBase  = "https://api.cloudflare.com/client/v4"
	cloudflareTokenEnv = "CLOUDFLARE_API_TOKEN"

	// cloudflareAutoTTL is the value Cloudflare uses for "automatic" TTL. It is
	// the only TTL accepted for proxied records.
	cloudflareAutoTTL = 1
)

// CloudflareDNSUpdater implements DNSUpdater against the Cloudflare API v4.
// DomainConfig.ZoneName holds the Cloudflare zone ID for these records.
type CloudflareDNSUpdater struct {
	Token  string
	Client *http.Client
}

// cfRecord is one entry of the Cloudflare DNS record API.
type cfRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int64  `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

// cfError is one entry of the Cloudflare API "errors" array.
type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// cfResponse is the envelope every Cloudflare API v4 call returns.
type cfResponse struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

// NewCloudflareDNSUpdater builds a Cloudflare client from an API token.
func NewCloudflareDNSUpdater(token string) (*CloudflareDNSUpdater, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("configuration contains %q records but %s is not set", ProviderCloudflare, cloudflareTokenEnv)
	}
	return &CloudflareDNSUpdater{
		Token:  strings.TrimSpace(token),
		Client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// do performs one Cloudflare API call and unmarshals the envelope. body may be
// nil for GET requests.
func (c *CloudflareDNSUpdater) do(method, path string, body any) (*cfResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to encode request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, cloudflareAPIBase+path, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare API request failed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read cloudflare API response: %w", err)
	}

	var parsed cfResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse cloudflare API response (status %d): %w", resp.StatusCode, err)
	}

	if !parsed.Success {
		return nil, fmt.Errorf("cloudflare API error (status %d): %s", resp.StatusCode, formatCFErrors(parsed.Errors))
	}

	return &parsed, nil
}

// formatCFErrors renders the Cloudflare errors array as a single string.
func formatCFErrors(errs []cfError) string {
	if len(errs) == 0 {
		return "no error detail returned"
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("%d: %s", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}

// cloudflareRecordName strips the trailing dot that GCP requires and
// Cloudflare rejects, so one config format works for both providers.
func cloudflareRecordName(recordName string) string {
	return strings.TrimSuffix(recordName, ".")
}

// cloudflareTTL returns the TTL to send. Cloudflare requires the automatic TTL
// for proxied records and rejects any explicit value.
func cloudflareTTL(rec DomainConfig) int64 {
	if rec.Proxied || rec.TTL <= 0 {
		return cloudflareAutoTTL
	}
	return rec.TTL
}

// findRecord returns the existing record matching the config, or nil if the
// zone has no such record.
func (c *CloudflareDNSUpdater) findRecord(rec DomainConfig) (*cfRecord, error) {
	query := url.Values{}
	query.Set("type", rec.RecordType)
	query.Set("name", cloudflareRecordName(rec.RecordName))

	path := fmt.Sprintf("/zones/%s/dns_records?%s", url.PathEscape(rec.ZoneName), query.Encode())
	resp, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var records []cfRecord
	if err := json.Unmarshal(resp.Result, &records); err != nil {
		return nil, fmt.Errorf("failed to parse cloudflare record list: %w", err)
	}

	if len(records) == 0 {
		return nil, nil
	}
	return &records[0], nil
}

// GetCurrentDNSRecordIP fetches the current IP from the Cloudflare record.
func (c *CloudflareDNSUpdater) GetCurrentDNSRecordIP(rec DomainConfig) (string, error) {
	found, err := c.findRecord(rec)
	if err != nil {
		return "", err
	}
	if found == nil {
		return "", fmt.Errorf("no %s record found for %s in cloudflare zone %s", rec.RecordType, rec.RecordName, rec.ZoneName)
	}
	if found.Content == "" {
		return "", fmt.Errorf("%s record found for %s but no IP address data", rec.RecordType, rec.RecordName)
	}
	return found.Content, nil
}

// UpdateDNSRecord points the Cloudflare record at the new IP address.
func (c *CloudflareDNSUpdater) UpdateDNSRecord(rec DomainConfig, ipAddress string) error {
	name := cloudflareRecordName(rec.RecordName)
	ttl := cloudflareTTL(rec)

	log.Printf("Attempting to update Cloudflare record: Zone=%s, Name=%s, Type=%s, IP=%s, TTL=%d, Proxied=%t",
		rec.ZoneName, name, rec.RecordType, ipAddress, ttl, rec.Proxied)

	existing, err := c.findRecord(rec)
	if err != nil {
		return err
	}

	payload := cfRecord{
		Type:    rec.RecordType,
		Name:    name,
		Content: ipAddress,
		TTL:     ttl,
		Proxied: rec.Proxied,
	}

	if existing == nil {
		return createCloudflareRecord(c, rec, payload)
	}

	path := fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(rec.ZoneName), url.PathEscape(existing.ID))
	if _, err := c.do(http.MethodPut, path, payload); err != nil {
		return fmt.Errorf("failed to update cloudflare record %s: %w", name, err)
	}

	log.Printf("Cloudflare record %s updated to %s", name, ipAddress)
	return nil
}

// createCloudflareRecord adds a record the zone does not have yet. This matches
// the GCP path, which also adds a record set when none exists.
func createCloudflareRecord(c *CloudflareDNSUpdater, rec DomainConfig, payload cfRecord) error {
	log.Printf("No existing %s record for %s in cloudflare zone %s. Creating it.", rec.RecordType, payload.Name, rec.ZoneName)

	path := fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(rec.ZoneName))
	if _, err := c.do(http.MethodPost, path, payload); err != nil {
		return fmt.Errorf("failed to create cloudflare record %s: %w", payload.Name, err)
	}

	log.Printf("Cloudflare record %s created with %s", payload.Name, payload.Content)
	return nil
}
