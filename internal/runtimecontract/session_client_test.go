package runtimecontract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateAndRevokeKitSessionReplacement(t *testing.T) {
	const oldSessionID = "01K1X4Y5Z6A7B8C9D0E1F2G3H5"
	var created, revoked bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/runtime/sessions":
			created = true
			if request.Header.Get("Idempotency-Key") != "renew-test-001" {
				t.Errorf("idempotency key = %q", request.Header.Get("Idempotency-Key"))
			}
			if request.Header.Get("X-Keydris-Replaces-Kit") != oldSessionID {
				t.Errorf("replacement header = %q", request.Header.Get("X-Keydris-Replaces-Kit"))
			}
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["agent_id"] != "agent-1" || body["session_handle"] != "handle-1" {
				t.Errorf("request body = %v", body)
			}
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, canonicalKITSession)
		case "/runtime/sessions/01K1X4Y5Z6A7B8C9D0E1F2G3H4/revoke":
			revoked = true
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	session, err := CreateKitSession(context.Background(), server.Client(), server.URL, CreateKitSessionInput{
		AgentID:           "agent-1",
		SessionHandle:     "handle-1",
		IdempotencyKey:    "renew-test-001",
		ReplacesSessionID: oldSessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || session.SessionID != "01K1X4Y5Z6A7B8C9D0E1F2G3H4" {
		t.Fatalf("session was not created: %+v", session)
	}
	if err := RevokeKitSession(context.Background(), server.Client(), server.URL, session.SessionID); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("session was not revoked")
	}
}
