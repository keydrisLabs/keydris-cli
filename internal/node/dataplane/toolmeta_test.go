package dataplane

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestApplyRequestMetadataCapturesJSONAndRestoresBody(t *testing.T) {
	const body = `{"repository":"keydris-cli","force":false}`
	req, err := http.NewRequest(http.MethodPost, "https://api.example.test/v1/deploy?token=secret", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	var flow Flow
	if err := applyRequestMetadata(&flow, req); err != nil {
		t.Fatal(err)
	}
	if flow.ToolCall != "POST /v1/deploy" {
		t.Fatalf("ToolCall = %q", flow.ToolCall)
	}
	if string(flow.ToolParams) != body {
		t.Fatalf("ToolParams = %s", flow.ToolParams)
	}

	gotBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != body {
		t.Fatalf("restored body = %q", gotBody)
	}
}

func TestApplyRequestMetadataPromotesMCPToolCall(t *testing.T) {
	const body = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_users","arguments":{"limit":3}}}`
	req, err := http.NewRequest(http.MethodPost, "https://mockmcp.io/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	var flow Flow
	if err := applyRequestMetadata(&flow, req); err != nil {
		t.Fatal(err)
	}
	if flow.ToolCall != "list_users" {
		t.Fatalf("ToolCall = %q", flow.ToolCall)
	}
	if string(flow.ToolParams) != `{"limit":3}` {
		t.Fatalf("ToolParams = %s", flow.ToolParams)
	}

	gotBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != body {
		t.Fatalf("restored body = %q", gotBody)
	}
}

func TestApplyRequestMetadataKeepsHTTPFallbackForMCPDiscovery(t *testing.T) {
	const body = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	req, err := http.NewRequest(http.MethodPost, "https://mockmcp.io/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	var flow Flow
	if err := applyRequestMetadata(&flow, req); err != nil {
		t.Fatal(err)
	}
	if flow.ToolCall != "POST /mcp" {
		t.Fatalf("ToolCall = %q", flow.ToolCall)
	}
	if string(flow.ToolParams) != body {
		t.Fatalf("ToolParams = %s", flow.ToolParams)
	}
}

func TestApplyRequestMetadataOmitsNonJSONBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://example.test/upload", strings.NewReader("opaque payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")

	var flow Flow
	if err := applyRequestMetadata(&flow, req); err != nil {
		t.Fatal(err)
	}
	if flow.ToolCall != "POST /upload" {
		t.Fatalf("ToolCall = %q", flow.ToolCall)
	}
	if flow.ToolParams != nil {
		t.Fatalf("ToolParams = %s, want nil", flow.ToolParams)
	}
}

func TestApplyRequestMetadataRejectsInvalidJSON(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://example.test/run", strings.NewReader(`{"broken":`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/problem+json")

	var flow Flow
	if err := applyRequestMetadata(&flow, req); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}
