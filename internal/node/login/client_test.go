package login

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/platform"
	"github.com/keydrisLabs/keydris-cli/internal/secureurl"
)

// clientMetadata is the self-reported platform block added to /identity/sign,
// /identity/enroll and /identity/renew. The assertions follow the host so the
// test is meaningful on every platform the CLI ships for.
func TestClientMetadataMatchesHostAndBuild(t *testing.T) {
	metadata := clientMetadata()
	if metadata["platform"] != runtime.GOOS {
		t.Errorf("platform = %q, want %q", metadata["platform"], runtime.GOOS)
	}
	if metadata["arch"] != runtime.GOARCH {
		t.Errorf("arch = %q, want %q", metadata["arch"], runtime.GOARCH)
	}
	if metadata["cli_version"] != ClientVersion {
		t.Errorf("cli_version = %q, want %q", metadata["cli_version"], ClientVersion)
	}
	environment := platform.Current()
	if environment.WSL == "" {
		if variant, present := metadata["platform_variant"]; present {
			t.Errorf("platform_variant = %q without WSL", variant)
		}
	} else if metadata["platform_variant"] != "wsl"+environment.WSL {
		t.Errorf("platform_variant = %q, want %q", metadata["platform_variant"], "wsl"+environment.WSL)
	}
}

func TestWithClientMetadataPreservesPayload(t *testing.T) {
	payload := map[string]string{"csr": "csr-pem", "device_name": "laptop"}
	got := withClientMetadata(payload)
	if got["csr"] != "csr-pem" || got["device_name"] != "laptop" {
		t.Fatalf("existing fields were dropped: %v", got)
	}
	for _, key := range []string{"platform", "arch", "cli_version"} {
		if got[key] == "" {
			t.Errorf("missing metadata field %q: %v", key, got)
		}
	}
}

func TestSignCSRSendsClientMetadata(t *testing.T) {
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/identity/sign" {
			http.NotFound(writer, request)
			return
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(signResponse{Certificate: "cert-pem", AgentID: "agent-1"})
	}))
	defer server.Close()

	response, err := signCSR(server.URL, "test-token", []byte("csr-pem"), "device-1", "laptop", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if response.Certificate != "cert-pem" {
		t.Fatalf("response = %+v", response)
	}
	if body["csr"] != "csr-pem" || body["device_name"] != "laptop" ||
		body["device_id"] != "device-1" || body["agent_id"] != "agent-1" {
		t.Errorf("identity fields = %v", body)
	}
	if body["platform"] != runtime.GOOS || body["arch"] != runtime.GOARCH || body["cli_version"] != ClientVersion {
		t.Errorf("client metadata = %v", body)
	}
}

func TestSignCSRRefusesPlaintextNonLoopback(t *testing.T) {
	t.Setenv(secureurl.AllowInsecureEnv, "")
	for _, controlURL := range []string{
		"http://control.internal:8081",
		"http://10.0.0.5",
		"ftp://control.internal",
	} {
		if _, err := signCSR(controlURL, "test-token", nil, "", "", ""); err == nil {
			t.Errorf("signCSR(%q) accepted a token-bearing plaintext URL", controlURL)
		}
	}
}
