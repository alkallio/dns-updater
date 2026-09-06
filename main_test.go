package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// TestIsValidIP tests IP address validation
func TestIsValidIP(t *testing.T) {
	tests := []struct {
		name  string
		ip    string
		valid bool
	}{
		{"Valid IPv4", "192.168.1.1", true},
		{"Valid IPv4 public", "8.8.8.8", true},
		{"Valid IPv6", "2001:0db8:85a3::8a2e:0370:7334", true},
		{"Invalid format", "999.999.999.999", false},
		{"Not an IP", "not-an-ip", false},
		{"Empty string", "", false},
		{"Incomplete IPv4", "192.168.1", false},
		{"Zero IP", "0.0.0.0", true},
		{"Max IPv4", "255.255.255.255", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidIP(tt.ip)
			if result != tt.valid {
				t.Errorf("isValidIP(%q) = %v, want %v", tt.ip, result, tt.valid)
			}
		})
	}
}

// TestExtractProjectID tests project ID extraction from service account JSON
func TestExtractProjectID(t *testing.T) {
	tests := []struct {
		name      string
		input     []byte
		wantID    string
		wantError bool
	}{
		{
			name:      "Valid JSON with project_id",
			input:     []byte(`{"project_id":"test-project-123","type":"service_account"}`),
			wantID:    "test-project-123",
			wantError: false,
		},
		{
			name:      "Missing project_id field",
			input:     []byte(`{"type":"service_account"}`),
			wantID:    "",
			wantError: true,
		},
		{
			name:      "Empty project_id",
			input:     []byte(`{"project_id":"","type":"service_account"}`),
			wantID:    "",
			wantError: true,
		},
		{
			name:      "Invalid JSON",
			input:     []byte(`{invalid json`),
			wantID:    "",
			wantError: true,
		},
		{
			name:      "Empty byte array",
			input:     []byte{},
			wantID:    "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, err := extractProjectID(tt.input)
			if (err != nil) != tt.wantError {
				t.Errorf("extractProjectID() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if gotID != tt.wantID {
				t.Errorf("extractProjectID() = %q, want %q", gotID, tt.wantID)
			}
		})
	}
}

// MockDNSUpdater is a mock implementation of DNSUpdater for testing
type MockDNSUpdater struct {
	GetCurrentIPFunc func(rec DomainConfig) (string, error)
	UpdateRecordFunc func(rec DomainConfig, ipAddress string) error
}

func (m *MockDNSUpdater) GetCurrentDNSRecordIP(rec DomainConfig) (string, error) {
	if m.GetCurrentIPFunc != nil {
		return m.GetCurrentIPFunc(rec)
	}
	return "", fmt.Errorf("not implemented")
}

func (m *MockDNSUpdater) UpdateDNSRecord(rec DomainConfig, ipAddress string) error {
	if m.UpdateRecordFunc != nil {
		return m.UpdateRecordFunc(rec, ipAddress)
	}
	return fmt.Errorf("not implemented")
}

// TestParseIPFromKeyValue tests parsing IP from Cloudflare trace format
func TestParseIPFromKeyValue(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantIP string
	}{
		{
			name: "Cloudflare trace format",
			input: `fl=88f182
h=cloudflare.com
ip=79.107.37.129
ts=1762166320.116
visit_scheme=https
uag=Mozilla/5.0
colo=ATH
sliver=none`,
			wantIP: "79.107.37.129",
		},
		{
			name: "IPv6 address",
			input: `fl=88f182
ip=2001:0db8:85a3::8a2e:0370:7334
ts=1762166320.116`,
			wantIP: "2001:0db8:85a3::8a2e:0370:7334",
		},
		{
			name:   "No IP field",
			input:  "fl=88f182\nts=1762166320.116",
			wantIP: "",
		},
		{
			name:   "Empty string",
			input:  "",
			wantIP: "",
		},
		{
			name:   "IP field with spaces",
			input:  "  ip=192.168.1.1  \nother=value",
			wantIP: "192.168.1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIP := parseIPFromKeyValue(tt.input)
			if gotIP != tt.wantIP {
				t.Errorf("parseIPFromKeyValue() = %q, want %q", gotIP, tt.wantIP)
			}
		})
	}
}

// TestProcessRecord tests the core business logic for DNS updates
func TestProcessRecord(t *testing.T) {
	config := DomainConfig{
		Provider:   ProviderGCP,
		ZoneName:   "example-zone",
		RecordName: "test.example.com.",
		RecordType: "A",
		TTL:        300,
	}
	key := config.Key()

	t.Run("No change - current IP equals last known IP", func(t *testing.T) {
		lastKnownIPs := map[string]string{key: "1.2.3.4"}
		currentIP := "1.2.3.4"

		mock := &MockDNSUpdater{}
		// No functions should be called

		processRecord(config, currentIP, lastKnownIPs, mock)

		// Verify lastKnownIPs unchanged
		if lastKnownIPs[key] != "1.2.3.4" {
			t.Errorf("lastKnownIPs should remain unchanged")
		}
	})

	t.Run("DNS matches - no update needed but update lastKnownIPs", func(t *testing.T) {
		lastKnownIPs := map[string]string{key: "1.2.3.4"}
		currentIP := "5.6.7.8"

		updateCalled := false
		mock := &MockDNSUpdater{
			GetCurrentIPFunc: func(rec DomainConfig) (string, error) {
				return "5.6.7.8", nil // DNS already has the current IP
			},
			UpdateRecordFunc: func(rec DomainConfig, ipAddress string) error {
				updateCalled = true
				return nil
			},
		}

		processRecord(config, currentIP, lastKnownIPs, mock)

		if updateCalled {
			t.Error("UpdateDNSRecord should not be called when DNS already matches")
		}
		if lastKnownIPs[key] != "5.6.7.8" {
			t.Errorf("lastKnownIPs should be updated to %q, got %q", "5.6.7.8", lastKnownIPs[key])
		}
	})

	t.Run("Update needed - successful DNS update", func(t *testing.T) {
		lastKnownIPs := map[string]string{key: "1.2.3.4"}
		currentIP := "5.6.7.8"

		updateCalled := false
		mock := &MockDNSUpdater{
			GetCurrentIPFunc: func(rec DomainConfig) (string, error) {
				return "1.2.3.4", nil // DNS has old IP
			},
			UpdateRecordFunc: func(rec DomainConfig, ipAddress string) error {
				updateCalled = true
				if ipAddress != "5.6.7.8" {
					t.Errorf("UpdateDNSRecord called with IP %q, want %q", ipAddress, "5.6.7.8")
				}
				return nil
			},
		}

		processRecord(config, currentIP, lastKnownIPs, mock)

		if !updateCalled {
			t.Error("UpdateDNSRecord should be called")
		}
		if lastKnownIPs[key] != "5.6.7.8" {
			t.Errorf("lastKnownIPs should be updated to %q, got %q", "5.6.7.8", lastKnownIPs[key])
		}
	})

	t.Run("DNS lookup fails - proceed with update", func(t *testing.T) {
		lastKnownIPs := map[string]string{}
		currentIP := "5.6.7.8"

		updateCalled := false
		mock := &MockDNSUpdater{
			GetCurrentIPFunc: func(rec DomainConfig) (string, error) {
				return "", fmt.Errorf("DNS lookup failed")
			},
			UpdateRecordFunc: func(rec DomainConfig, ipAddress string) error {
				updateCalled = true
				return nil
			},
		}

		processRecord(config, currentIP, lastKnownIPs, mock)

		if !updateCalled {
			t.Error("UpdateDNSRecord should be called even when DNS lookup fails")
		}
		if lastKnownIPs[key] != "5.6.7.8" {
			t.Errorf("lastKnownIPs should be updated after successful update")
		}
	})

	t.Run("DNS update fails - do not update lastKnownIPs", func(t *testing.T) {
		lastKnownIPs := map[string]string{key: "1.2.3.4"}
		currentIP := "5.6.7.8"

		mock := &MockDNSUpdater{
			GetCurrentIPFunc: func(rec DomainConfig) (string, error) {
				return "1.2.3.4", nil
			},
			UpdateRecordFunc: func(rec DomainConfig, ipAddress string) error {
				return fmt.Errorf("update failed")
			},
		}

		processRecord(config, currentIP, lastKnownIPs, mock)

		if lastKnownIPs[key] != "1.2.3.4" {
			t.Errorf("lastKnownIPs should NOT be updated when update fails, got %q", lastKnownIPs[key])
		}
	})
}

// TestDomainConfigKey verifies that records differing only by provider or zone
// get distinct keys, so their last known IPs do not collide.
func TestDomainConfigKey(t *testing.T) {
	gcp := DomainConfig{Provider: ProviderGCP, ZoneName: "z1", RecordName: "a.example.com.", RecordType: "A"}
	cf := DomainConfig{Provider: ProviderCloudflare, ZoneName: "z1", RecordName: "a.example.com.", RecordType: "A"}
	aaaa := DomainConfig{Provider: ProviderGCP, ZoneName: "z1", RecordName: "a.example.com.", RecordType: "AAAA"}

	if gcp.Key() == cf.Key() {
		t.Error("records from different providers must not share a key")
	}
	if gcp.Key() == aaaa.Key() {
		t.Error("records of different types must not share a key")
	}
}

// TestCloudflareRecordName checks that the GCP trailing dot is stripped.
func TestCloudflareRecordName(t *testing.T) {
	tests := map[string]string{
		"sub.example.com.": "sub.example.com",
		"sub.example.com":  "sub.example.com",
		"example.com.":     "example.com",
	}
	for input, want := range tests {
		if got := cloudflareRecordName(input); got != want {
			t.Errorf("cloudflareRecordName(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestCloudflareTTL checks that proxied records force the automatic TTL.
func TestCloudflareTTL(t *testing.T) {
	tests := []struct {
		name string
		rec  DomainConfig
		want int64
	}{
		{"explicit TTL", DomainConfig{TTL: 300}, 300},
		{"proxied forces auto", DomainConfig{TTL: 300, Proxied: true}, cloudflareAutoTTL},
		{"zero TTL means auto", DomainConfig{TTL: 0}, cloudflareAutoTTL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cloudflareTTL(tt.rec); got != tt.want {
				t.Errorf("cloudflareTTL() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestDomainConfigJSONTags verifies that the snake_case keys documented in the
// README actually populate the struct.
func TestDomainConfigJSONTags(t *testing.T) {
	input := []byte(`{"provider":"cloudflare","zone_name":"abc123","record_name":"sub.example.com.","record_type":"A","ttl":300,"proxied":true}`)
	var got DomainConfig
	if err := json.Unmarshal(input, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	want := DomainConfig{Provider: "cloudflare", ZoneName: "abc123", RecordName: "sub.example.com.", RecordType: "A", TTL: 300, Proxied: true}
	if got != want {
		t.Errorf("parsed %+v, want %+v", got, want)
	}
}

// TestProviderFor checks the strict provider policy: only known providers are
// accepted, and a missing field is an error rather than a default.
func TestProviderFor(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		want      string
		wantError bool
	}{
		{"gcp", ProviderGCP, ProviderGCP, false},
		{"cloudflare", ProviderCloudflare, ProviderCloudflare, false},
		{"missing", "", "", true},
		{"unknown", "route53", "", true},
		{"wrong case", "GCP", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := providerFor(DomainConfig{Provider: tt.provider})
			if (err != nil) != tt.wantError {
				t.Fatalf("providerFor(%q) error = %v, wantError %v", tt.provider, err, tt.wantError)
			}
			if got != tt.want {
				t.Errorf("providerFor(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

// TestBuildUpdatersRejectsBadProvider verifies that a bad provider fails at
// startup instead of surfacing later as a skipped record.
func TestBuildUpdatersRejectsBadProvider(t *testing.T) {
	domains := []DomainConfig{{Provider: "route53", RecordName: "a.example.com."}}
	if _, err := buildUpdaters(context.Background(), domains, nil); err == nil {
		t.Error("buildUpdaters should reject an unknown provider")
	}
}

// TestBuildUpdatersNoGCPKey verifies that a gcp record without a service
// account key argument is reported as a usage error.
func TestBuildUpdatersNoGCPKey(t *testing.T) {
	domains := []DomainConfig{{Provider: ProviderGCP, RecordName: "a.example.com."}}
	if _, err := buildUpdaters(context.Background(), domains, nil); err == nil {
		t.Error("buildUpdaters should require a service account key for gcp records")
	}
}

// TestBuildUpdatersNoCloudflareToken verifies that a cloudflare record without
// a token is reported at startup.
func TestBuildUpdatersNoCloudflareToken(t *testing.T) {
	t.Setenv(cloudflareTokenEnv, "")
	domains := []DomainConfig{{Provider: ProviderCloudflare, RecordName: "a.example.com."}}
	if _, err := buildUpdaters(context.Background(), domains, nil); err == nil {
		t.Error("buildUpdaters should require CLOUDFLARE_API_TOKEN for cloudflare records")
	}
}

// TestBuildUpdatersCloudflareOnly verifies that a cloudflare-only config needs
// no GCP service account key.
func TestBuildUpdatersCloudflareOnly(t *testing.T) {
	t.Setenv(cloudflareTokenEnv, "test-token")
	domains := []DomainConfig{{Provider: ProviderCloudflare, RecordName: "a.example.com."}}

	updaters, err := buildUpdaters(context.Background(), domains, nil)
	if err != nil {
		t.Fatalf("buildUpdaters() error = %v", err)
	}
	if _, ok := updaters[ProviderGCP]; ok {
		t.Error("GCP client should not be initialized for a cloudflare-only config")
	}
	if _, ok := updaters[ProviderCloudflare]; !ok {
		t.Error("cloudflare client should be initialized")
	}
}
