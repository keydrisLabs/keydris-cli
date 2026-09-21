package runtimecontract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReportMCPInventory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/runtime/devices/mcp-inventory" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong inventory request")
		}
		var report MCPInventoryReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		if report.Runtime != "codex" || report.Entries == nil {
			t.Error("missing inventory metadata")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	err := ReportMCPInventory(context.Background(), server.Client(), server.URL, MCPInventoryReport{SchemaVersion: 1, Runtime: "codex", ObservedAt: "2026-09-20T12:00:00Z", Entries: []MCPInventoryEntry{}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInventoryRefusesRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			t.Error("followed inventory redirect")
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	if err := ReportMCPInventory(context.Background(), server.Client(), server.URL, MCPInventoryReport{}); err == nil {
		t.Fatal("redirect accepted")
	}
}

// TestReportMCPInventoryFailurePaths pins that a report is never silently
// dropped: a missing client, an unusable runtime URL, a rejected status, and an
// unreachable control plane all surface an error to the caller.
func TestReportMCPInventoryFailurePaths(t *testing.T) {
	if err := ReportMCPInventory(context.Background(), nil, "https://example.com", MCPInventoryReport{}); err == nil {
		t.Fatal("nil inventory client accepted")
	}
	if err := ReportMCPInventory(context.Background(), http.DefaultClient, "://bad", MCPInventoryReport{}); err == nil {
		t.Fatal("invalid runtime URL accepted")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	baseURL := server.URL
	if err := ReportMCPInventory(context.Background(), server.Client(), baseURL, MCPInventoryReport{}); err == nil {
		t.Fatal("HTTP failure accepted")
	}
	server.Close()
	if err := ReportMCPInventory(context.Background(), server.Client(), baseURL, MCPInventoryReport{}); err == nil {
		t.Fatal("unreachable control plane accepted")
	}
}
