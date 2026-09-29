package runtimecontract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// MCPInventoryEntry deliberately excludes headers, credentials, environment,
// commands, arguments, and local filesystem paths.
type MCPInventoryEntry struct {
	Name             string  `json:"name"`
	Transport        string  `json:"transport"`
	Endpoint         *string `json:"endpoint"`
	EndpointRedacted bool    `json:"endpoint_redacted"`
	Managed          bool    `json:"managed"`
	Enabled          bool    `json:"enabled"`
}

type MCPInventoryReport struct {
	SchemaVersion int                 `json:"schema_version"`
	Runtime       string              `json:"runtime"`
	ObservedAt    string              `json:"observed_at"`
	Entries       []MCPInventoryEntry `json:"entries"`
}

// ReportMCPInventory uses the existing device certificate. The server derives
// device and organization identity from mTLS, never from client-supplied IDs.
func ReportMCPInventory(ctx context.Context, client *http.Client, baseURL string, report MCPInventoryReport) error {
	if client == nil {
		return fmt.Errorf("missing inventory client")
	}
	endpoint, err := runtimeSessionURL(baseURL, "/runtime/devices/mcp-inventory")
	if err != nil {
		return err
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	// Do not forward a device report or its identity to redirected destinations.
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := bounded.Do(request)
	if err != nil {
		return fmt.Errorf("inventory delivery failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("inventory returned HTTP %d", response.StatusCode)
	}
	return nil
}
