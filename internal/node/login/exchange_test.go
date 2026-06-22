package login

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestExchangeConfidentialClient verifies that a configured client secret is
// sent as HTTP Basic auth on the token exchange (Cognito confidential client),
// and that the public-client case sends no Authorization header.
func TestExchangeConfidentialClient(t *testing.T) {
	cases := []struct {
		name      string
		secret    string
		wantUser  string
		wantBasic bool
	}{
		{name: "confidential", secret: "s3cret", wantUser: "client-123", wantBasic: true},
		{name: "public", secret: "", wantBasic: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sawBasic bool
			var gotUser, gotPass string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if u, p, ok := r.BasicAuth(); ok {
					sawBasic, gotUser, gotPass = true, u, p
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id_token":     "header.payload.sig",
					"access_token": "at",
				})
			}))
			defer ts.Close()

			opt := Options{TokenURL: ts.URL, ClientID: "client-123", ClientSecret: tc.secret}
			if _, err := exchangeCode(opt, "code", "verifier", "http://localhost:8400/callback"); err != nil {
				t.Fatalf("exchange: %v", err)
			}
			if sawBasic != tc.wantBasic {
				t.Fatalf("basic auth present = %v, want %v", sawBasic, tc.wantBasic)
			}
			if tc.wantBasic && (gotUser != tc.wantUser || gotPass != tc.secret) {
				t.Errorf("basic auth = %q:%q, want %q:%q", gotUser, gotPass, tc.wantUser, tc.secret)
			}
		})
	}
}
