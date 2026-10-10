package runtimecontract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const approvalTestID = "123e4567-e89b-42d3-a456-426614174000"

func TestGetApprovalStatusValidatesIdentityAndAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != ApprovalStatusEndpointPath {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("request_id") != "cli-request" {
			t.Fatalf("request_id = %q", request.URL.Query().Get("request_id"))
		}
		if request.Header.Get("Authorization") != "Bearer kit-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		fmt.Fprintf(writer, `{"approval_id":%q,"request_id":"cli-request","status":"approved","expires_at":"2030-01-01T00:00:00Z","resolved_at":"2029-12-31T23:59:00Z"}`, approvalTestID)
	}))
	defer server.Close()

	status, err := GetApprovalStatus(
		context.Background(), server.Client(), server.URL, "kit-token", "cli-request",
	)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != ApprovalApproved || status.ApprovalID != approvalTestID {
		t.Fatalf("unexpected approval status: %#v", status)
	}
}

func TestGetApprovalStatusRejectsMalformedResponses(t *testing.T) {
	tests := map[string]string{
		"wrong request":           `{"approval_id":"123e4567-e89b-42d3-a456-426614174000","request_id":"other","status":"approved","expires_at":"2030-01-01T00:00:00Z","resolved_at":null}`,
		"unknown status":          `{"approval_id":"123e4567-e89b-42d3-a456-426614174000","request_id":"cli-request","status":"surprise","expires_at":"2030-01-01T00:00:00Z","resolved_at":null}`,
		"duplicate key":           `{"approval_id":"123e4567-e89b-42d3-a456-426614174000","request_id":"cli-request","status":"pending","status":"approved","expires_at":"2030-01-01T00:00:00Z","resolved_at":null}`,
		"invalid approval id":     `{"approval_id":"not-a-uuid","request_id":"cli-request","status":"approved","expires_at":"2030-01-01T00:00:00Z","resolved_at":null}`,
		"invalid expires_at":      `{"approval_id":"123e4567-e89b-42d3-a456-426614174000","request_id":"cli-request","status":"approved","expires_at":"tomorrow","resolved_at":null}`,
		"invalid resolved_at":     `{"approval_id":"123e4567-e89b-42d3-a456-426614174000","request_id":"cli-request","status":"approved","expires_at":"2030-01-01T00:00:00Z","resolved_at":"soon"}`,
		"pending with resolution": `{"approval_id":"123e4567-e89b-42d3-a456-426614174000","request_id":"cli-request","status":"pending","expires_at":"2030-01-01T00:00:00Z","resolved_at":"2029-12-31T23:59:00Z"}`,
		"wrong field type":        `{"approval_id":123,"request_id":"cli-request","status":"approved","expires_at":"2030-01-01T00:00:00Z","resolved_at":null}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(writer, body)
			}))
			defer server.Close()
			if _, err := GetApprovalStatus(
				context.Background(), server.Client(), server.URL, "kit-token", "cli-request",
			); err == nil {
				t.Fatal("malformed approval status was accepted")
			}
		})
	}
}

func TestWaitForApprovalReturnsOnlyForApproved(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		status := "pending"
		resolvedAt := "null"
		if requests > 1 {
			status = "approved"
			resolvedAt = `"2029-12-31T23:59:00Z"`
		}
		fmt.Fprintf(writer, `{"approval_id":%q,"request_id":"cli-request","status":%q,"expires_at":"2030-01-01T00:00:00Z","resolved_at":%s}`, approvalTestID, status, resolvedAt)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := WaitForApproval(ctx, server.Client(), server.URL, "kit-token", "cli-request"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("approval status requests = %d, want 2", requests)
	}

	rejected := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(writer, `{"approval_id":%q,"request_id":"cli-request","status":"rejected","expires_at":"2030-01-01T00:00:00Z","resolved_at":"2029-12-31T23:59:00Z"}`, approvalTestID)
	}))
	defer rejected.Close()
	err := WaitForApproval(context.Background(), rejected.Client(), rejected.URL, "kit-token", "cli-request")
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("rejected approval error = %v", err)
	}
}

func TestGetApprovalStatusRejectsInvalidInputs(t *testing.T) {
	client := &http.Client{}
	if _, err := GetApprovalStatus(
		context.Background(), client, "http://127.0.0.1:1", "", "cli-request",
	); err == nil {
		t.Fatal("missing runtime token was accepted")
	}
	if _, err := GetApprovalStatus(
		context.Background(), client, "http://127.0.0.1:1", "kit-token", "bad request id",
	); err == nil {
		t.Fatal("invalid request id was accepted")
	}
	if _, err := GetApprovalStatus(
		context.Background(), client, "", "kit-token", "cli-request",
	); err == nil {
		t.Fatal("invalid base url was accepted")
	}
}

func TestGetApprovalStatusRejectsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	if _, err := GetApprovalStatus(
		context.Background(), server.Client(), server.URL, "kit-token", "cli-request",
	); err == nil {
		t.Fatal("non-200 approval status was accepted")
	}

	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client, baseURL := unreachable.Client(), unreachable.URL
	unreachable.Close()
	if _, err := GetApprovalStatus(
		context.Background(), client, baseURL, "kit-token", "cli-request",
	); err == nil {
		t.Fatal("unreachable approval status was accepted")
	}
}

func TestWaitForApprovalRejectsTerminalStatuses(t *testing.T) {
	for _, status := range []ApprovalStatus{ApprovalExpired, ApprovalConsumed, ApprovalCancelled} {
		t.Run(string(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(writer, `{"approval_id":%q,"request_id":"cli-request","status":%q,"expires_at":"2030-01-01T00:00:00Z","resolved_at":"2029-12-31T23:59:00Z"}`, approvalTestID, status)
			}))
			defer server.Close()
			err := WaitForApproval(
				context.Background(), server.Client(), server.URL, "kit-token", "cli-request",
			)
			if err == nil || !strings.Contains(err.Error(), string(status)) {
				t.Fatalf("status %q error = %v", status, err)
			}
		})
	}
}

func TestWaitForApprovalStopsWhenContextCancelled(t *testing.T) {
	polled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		select {
		case polled <- struct{}{}:
		default:
		}
		fmt.Fprintf(writer, `{"approval_id":%q,"request_id":"cli-request","status":"pending","expires_at":"2030-01-01T00:00:00Z","resolved_at":null}`, approvalTestID)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- WaitForApproval(ctx, server.Client(), server.URL, "kit-token", "cli-request")
	}()
	select {
	case <-polled:
	case <-time.After(3 * time.Second):
		t.Fatal("approval status was never polled")
	}
	// Let the pending response reach the poll timer, then cancel so the wait
	// must stop instead of sleeping out the interval.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("cancelled wait error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitForApproval did not stop after cancellation")
	}
}
