package runtimecontract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func usageEvents(count int) []SessionUsageEvent {
	events := make([]SessionUsageEvent, count)
	for i := range events {
		events[i] = SessionUsageEvent{
			RequestID:   "llm-1",
			Provider:    "anthropic",
			Model:       "claude-opus-5",
			InputTokens: 1,
			OccurredAt:  "2026-09-14T10:00:00.000Z",
		}
	}
	return events
}

func TestReportSessionUsage(t *testing.T) {
	var gotAuth string
	var gotReport SessionUsageReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != SessionUsagePath {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotReport); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(SessionUsageReportResponse{
			SchemaVersion: SchemaVersion,
			Accepted:      len(gotReport.Events),
			Duplicates:    0,
		})
	}))
	defer server.Close()

	result, err := ReportSessionUsage(
		context.Background(), server.Client(), server.URL, "kit-token", usageEvents(2),
	)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if result.Accepted != 2 || result.Duplicates != 0 {
		t.Fatalf("result: %+v", result)
	}
	if gotAuth != "Bearer kit-token" {
		t.Fatalf("authorization: %q", gotAuth)
	}
	if gotReport.SchemaVersion != SchemaVersion || len(gotReport.Events) != 2 {
		t.Fatalf("report body: %+v", gotReport)
	}
}

func TestReportSessionUsageRejectsBadInput(t *testing.T) {
	if _, err := ReportSessionUsage(
		context.Background(), http.DefaultClient, "https://example.com", "", usageEvents(1),
	); err == nil {
		t.Fatal("a missing session token must be rejected")
	}
	if _, err := ReportSessionUsage(
		context.Background(), http.DefaultClient, "https://example.com", "kit", nil,
	); err == nil {
		t.Fatal("an empty batch must be rejected")
	}
	if _, err := ReportSessionUsage(
		context.Background(), http.DefaultClient, "https://example.com", "kit",
		usageEvents(MaxUsageEventsPerReport+1),
	); err == nil {
		t.Fatal("an oversized batch must be rejected")
	}
}

func TestReportSessionUsageRejectsInvalidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":99,"accepted":1,"duplicates":0}`))
	}))
	defer server.Close()

	if _, err := ReportSessionUsage(
		context.Background(), server.Client(), server.URL, "kit", usageEvents(1),
	); err == nil {
		t.Fatal("a wrong schema_version must be rejected")
	}
}
