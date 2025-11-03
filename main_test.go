package main

import (
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
	GetCurrentIPFunc func(projectID, zoneName, recordName, recordType string) (string, error)
	UpdateRecordFunc func(projectID, zoneName, recordName, recordType, ipAddress string, ttl int64) error
}

func (m *MockDNSUpdater) GetCurrentDNSRecordIP(projectID, zoneName, recordName, recordType string) (string, error) {
	if m.GetCurrentIPFunc != nil {
		return m.GetCurrentIPFunc(projectID, zoneName, recordName, recordType)
	}
	return "", fmt.Errorf("not implemented")
}

func (m *MockDNSUpdater) UpdateDNSRecord(projectID, zoneName, recordName, recordType, ipAddress string, ttl int64) error {
	if m.UpdateRecordFunc != nil {
		return m.UpdateRecordFunc(projectID, zoneName, recordName, recordType, ipAddress, ttl)
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
	projectID := "test-project"
	config := DomainConfig{
		ZoneName:   "example-zone",
		RecordName: "test.example.com.",
		RecordType: "A",
		TTL:        300,
	}

	t.Run("No change - current IP equals last known IP", func(t *testing.T) {
		lastKnownIPs := map[string]string{
			"test.example.com.": "1.2.3.4",
		}
		currentIP := "1.2.3.4"

		mock := &MockDNSUpdater{}
		// No functions should be called

		processRecord(projectID, config, currentIP, lastKnownIPs, mock)

		// Verify lastKnownIPs unchanged
		if lastKnownIPs["test.example.com."] != "1.2.3.4" {
			t.Errorf("lastKnownIPs should remain unchanged")
		}
	})

	t.Run("DNS matches - no update needed but update lastKnownIPs", func(t *testing.T) {
		lastKnownIPs := map[string]string{
			"test.example.com.": "1.2.3.4",
		}
		currentIP := "5.6.7.8"

		updateCalled := false
		mock := &MockDNSUpdater{
			GetCurrentIPFunc: func(projectID, zoneName, recordName, recordType string) (string, error) {
				return "5.6.7.8", nil // DNS already has the current IP
			},
			UpdateRecordFunc: func(projectID, zoneName, recordName, recordType, ipAddress string, ttl int64) error {
				updateCalled = true
				return nil
			},
		}

		processRecord(projectID, config, currentIP, lastKnownIPs, mock)

		if updateCalled {
			t.Error("UpdateDNSRecord should not be called when DNS already matches")
		}
		if lastKnownIPs["test.example.com."] != "5.6.7.8" {
			t.Errorf("lastKnownIPs should be updated to %q, got %q", "5.6.7.8", lastKnownIPs["test.example.com."])
		}
	})

	t.Run("Update needed - successful DNS update", func(t *testing.T) {
		lastKnownIPs := map[string]string{
			"test.example.com.": "1.2.3.4",
		}
		currentIP := "5.6.7.8"

		updateCalled := false
		mock := &MockDNSUpdater{
			GetCurrentIPFunc: func(projectID, zoneName, recordName, recordType string) (string, error) {
				return "1.2.3.4", nil // DNS has old IP
			},
			UpdateRecordFunc: func(projectID, zoneName, recordName, recordType, ipAddress string, ttl int64) error {
				updateCalled = true
				if ipAddress != "5.6.7.8" {
					t.Errorf("UpdateDNSRecord called with IP %q, want %q", ipAddress, "5.6.7.8")
				}
				return nil
			},
		}

		processRecord(projectID, config, currentIP, lastKnownIPs, mock)

		if !updateCalled {
			t.Error("UpdateDNSRecord should be called")
		}
		if lastKnownIPs["test.example.com."] != "5.6.7.8" {
			t.Errorf("lastKnownIPs should be updated to %q, got %q", "5.6.7.8", lastKnownIPs["test.example.com."])
		}
	})

	t.Run("DNS lookup fails - proceed with update", func(t *testing.T) {
		lastKnownIPs := map[string]string{}
		currentIP := "5.6.7.8"

		updateCalled := false
		mock := &MockDNSUpdater{
			GetCurrentIPFunc: func(projectID, zoneName, recordName, recordType string) (string, error) {
				return "", fmt.Errorf("DNS lookup failed")
			},
			UpdateRecordFunc: func(projectID, zoneName, recordName, recordType, ipAddress string, ttl int64) error {
				updateCalled = true
				return nil
			},
		}

		processRecord(projectID, config, currentIP, lastKnownIPs, mock)

		if !updateCalled {
			t.Error("UpdateDNSRecord should be called even when DNS lookup fails")
		}
		if lastKnownIPs["test.example.com."] != "5.6.7.8" {
			t.Errorf("lastKnownIPs should be updated after successful update")
		}
	})

	t.Run("DNS update fails - do not update lastKnownIPs", func(t *testing.T) {
		lastKnownIPs := map[string]string{
			"test.example.com.": "1.2.3.4",
		}
		currentIP := "5.6.7.8"

		mock := &MockDNSUpdater{
			GetCurrentIPFunc: func(projectID, zoneName, recordName, recordType string) (string, error) {
				return "1.2.3.4", nil
			},
			UpdateRecordFunc: func(projectID, zoneName, recordName, recordType, ipAddress string, ttl int64) error {
				return fmt.Errorf("update failed")
			},
		}

		processRecord(projectID, config, currentIP, lastKnownIPs, mock)

		if lastKnownIPs["test.example.com."] != "1.2.3.4" {
			t.Errorf("lastKnownIPs should NOT be updated when update fails, got %q", lastKnownIPs["test.example.com."])
		}
	})
}
