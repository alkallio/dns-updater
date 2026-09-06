package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cfTestServer serves a minimal Cloudflare API. list is the response to a
// record lookup; every write is recorded for inspection.
type cfTestServer struct {
	*httptest.Server
	listResult string
	method     string
	path       string
	body       string
}

func newCFTestServer(t *testing.T, listResult string) *cfTestServer {
	t.Helper()
	s := &cfTestServer{listResult: listResult}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"success":true,"errors":[],"result":` + s.listResult + `}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		s.method, s.path, s.body = r.Method, r.URL.Path, string(body)
		w.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func cfUpdaterFor(s *cfTestServer) *CloudflareDNSUpdater {
	return &CloudflareDNSUpdater{Token: "test-token", Client: s.Client(), BaseURL: s.URL}
}

var cfTestRecord = DomainConfig{
	Provider:   ProviderCloudflare,
	ZoneName:   "zone123",
	RecordName: "mail.example.com.",
	RecordType: "A",
	TTL:        300,
}

// TestCloudflareUpdateSendsContentOnly is the ownership boundary: the updater
// owns the value of a record, and must not assert the TTL or the proxy setting
// that the system managing the zone owns.
func TestCloudflareUpdateSendsContentOnly(t *testing.T) {
	server := newCFTestServer(t, `[{"id":"rec456","type":"A","name":"mail.example.com","content":"1.2.3.4","ttl":300,"proxied":false}]`)

	if err := cfUpdaterFor(server).UpdateDNSRecord(cfTestRecord, "5.6.7.8"); err != nil {
		t.Fatalf("UpdateDNSRecord() error = %v", err)
	}

	if server.method != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", server.method)
	}
	if server.path != "/zones/zone123/dns_records/rec456" {
		t.Errorf("path = %s, want /zones/zone123/dns_records/rec456", server.path)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(server.body), &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if sent["content"] != "5.6.7.8" {
		t.Errorf("content = %v, want 5.6.7.8", sent["content"])
	}
	for _, field := range []string{"ttl", "proxied", "type", "name"} {
		if _, present := sent[field]; present {
			t.Errorf("request must not carry %q; that field belongs to the zone's owner", field)
		}
	}
}

// TestCloudflareUpdateRefusesToCreate verifies that an absent record is
// reported, not created. Record existence belongs to whatever manages the zone.
func TestCloudflareUpdateRefusesToCreate(t *testing.T) {
	server := newCFTestServer(t, `[]`)

	err := cfUpdaterFor(server).UpdateDNSRecord(cfTestRecord, "5.6.7.8")
	if err == nil {
		t.Fatal("UpdateDNSRecord should refuse to create an absent record")
	}
	if server.method != "" {
		t.Errorf("no write should be sent, got %s %s", server.method, server.path)
	}
}

// TestCloudflareGetCurrentIP reads the published value back.
func TestCloudflareGetCurrentIP(t *testing.T) {
	server := newCFTestServer(t, `[{"id":"rec456","type":"A","name":"mail.example.com","content":"1.2.3.4","ttl":300}]`)

	got, err := cfUpdaterFor(server).GetCurrentDNSRecordIP(cfTestRecord)
	if err != nil {
		t.Fatalf("GetCurrentDNSRecordIP() error = %v", err)
	}
	if got != "1.2.3.4" {
		t.Errorf("GetCurrentDNSRecordIP() = %q, want %q", got, "1.2.3.4")
	}
}

// TestCloudflareGetCurrentIPAbsent reports a missing record as an error.
func TestCloudflareGetCurrentIPAbsent(t *testing.T) {
	server := newCFTestServer(t, `[]`)

	if _, err := cfUpdaterFor(server).GetCurrentDNSRecordIP(cfTestRecord); err == nil {
		t.Error("GetCurrentDNSRecordIP should report an absent record as an error")
	}
}

// TestCloudflareAPIError surfaces the API's own error text.
func TestCloudflareAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}]}`))
	}))
	defer server.Close()

	updater := &CloudflareDNSUpdater{Token: "bad", Client: server.Client(), BaseURL: server.URL}
	_, err := updater.GetCurrentDNSRecordIP(cfTestRecord)
	if err == nil {
		t.Fatal("expected an error from a failed API call")
	}
	if !strings.Contains(err.Error(), "Invalid access token") {
		t.Errorf("error should carry the API message, got %v", err)
	}
}

// TestNewCloudflareDNSUpdaterRequiresToken verifies the startup check.
func TestNewCloudflareDNSUpdaterRequiresToken(t *testing.T) {
	if _, err := NewCloudflareDNSUpdater("   "); err == nil {
		t.Error("a blank token must be rejected")
	}
}
