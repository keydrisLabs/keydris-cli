package runtimecontract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type CreateKitSessionInput struct {
	AgentID           string
	SessionHandle     string
	IdempotencyKey    string
	ReplacesSessionID string
}

// CreateKitSession creates or atomically replaces a runtime session through
// the canonical mTLS endpoint.
func CreateKitSession(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	input CreateKitSessionInput,
) (*KitSession, error) {
	if client == nil {
		return nil, fmt.Errorf("nil runtime session client")
	}
	if input.AgentID == "" || input.SessionHandle == "" {
		return nil, fmt.Errorf("runtime session requires agent and handle")
	}
	if !requestIDPattern.MatchString(input.IdempotencyKey) || len(input.IdempotencyKey) < 8 {
		return nil, fmt.Errorf("invalid runtime session idempotency key")
	}
	if input.ReplacesSessionID != "" && !ulidPattern.MatchString(input.ReplacesSessionID) {
		return nil, fmt.Errorf("invalid replacement session id")
	}
	endpoint, err := runtimeSessionURL(baseURL, "/runtime/sessions")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{
		"agent_id":       input.AgentID,
		"session_handle": input.SessionHandle,
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", input.IdempotencyKey)
	if input.ReplacesSessionID != "" {
		request.Header.Set("X-Keydris-Replaces-Kit", input.ReplacesSessionID)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return nil, fmt.Errorf(
			"runtime session endpoint %s: %s",
			response.Status,
			bytes.TrimSpace(responseBody),
		)
	}
	return DecodeKitSession(response.Body)
}

func RevokeKitSession(
	ctx context.Context,
	client *http.Client,
	baseURL,
	sessionID string,
) error {
	if client == nil {
		return fmt.Errorf("nil runtime session client")
	}
	if !ulidPattern.MatchString(sessionID) {
		return fmt.Errorf("invalid runtime session id")
	}
	endpoint, err := runtimeSessionURL(baseURL, "/runtime/sessions/"+sessionID+"/revoke")
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("revoke returned %s", response.Status)
	}
	return nil
}

func runtimeSessionURL(baseURL, path string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" ||
		(base.Scheme != "https" && base.Scheme != "http") {
		return "", fmt.Errorf("invalid Keydris runtime URL")
	}
	base.Path = path
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}
