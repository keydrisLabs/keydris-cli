// Package login implements the node side of `keydris login`: the browser-based
// OAuth 2.0 Authorization-Code + PKCE flow against the control plane, followed
// by local key generation and a CSR that the control plane signs into a client
// certificate. The certificate (and its private key) are stored locally and
// later presented by the daemon over mTLS — the private key never leaves the
// machine.
//
// Flow (mirrors internal/control/authn on the server):
//
//  1. generate a PKCE verifier + S256 challenge and a random state;
//  2. start a loopback HTTP listener on 127.0.0.1:<random> for the redirect;
//  3. open the browser at <control>/oauth/authorize?...;
//  4. receive ?code=&state= on the loopback, validating state;
//  5. POST <control>/oauth/token to exchange code+verifier for an access token;
//  6. generate an EC key + CSR, POST <control>/identity/sign with the token;
//  7. persist key + cert + pinned CA + whoami metadata under IdentityDir.
package login

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Options drives Run.
type Options struct {
	// ControlURL is the control-plane base URL; it signs the CSR into the client
	// certificate after the user authenticates.
	ControlURL string
	// IdentityDir is where the resulting key/cert/metadata are written.
	IdentityDir string

	// AuthorizeURL / TokenURL are the OAuth provider endpoints. When empty they
	// default to the control plane's built-in mock IdP, so the flow works
	// offline; for AWS Cognito they point at the Hosted UI endpoints.
	AuthorizeURL string
	TokenURL     string
	// ClientID is the OAuth client id (defaults to the mock "keydris-cli").
	ClientID string
	// ClientSecret, when set, authenticates the token exchange via HTTP Basic
	// (confidential Cognito app client). Empty for a public client.
	ClientSecret string
	// Scopes is the space-separated scope set requested (e.g. "openid email").
	Scopes string
	// RedirectURL, when set, is a fixed loopback callback the CLI binds and the
	// provider must have registered (Cognito requires an exact match). When
	// empty, an ephemeral 127.0.0.1:<random> callback is used (mock IdP).
	RedirectURL string

	// LoginHint pre-fills the consent page's email field (mock IdP only).
	LoginHint string
	// NoBrowser prints the URL instead of launching a browser (headless/CI).
	NoBrowser bool
	// Timeout bounds the wait for the browser redirect.
	Timeout time.Duration
	// Open opens a URL in the browser; defaults to the platform opener. Injected
	// for tests.
	Open func(string) error
}

const defaultClientID = "keydris-cli"

// Run executes the full browser login and returns the stored identity.
func Run(opt Options) (*Identity, error) {
	opt.applyDefaults()
	if opt.Open == nil {
		opt.Open = openBrowser
	}

	verifier := randURL(32)
	challenge := s256(verifier)
	state := randURL(16)

	ln, redirectURI, callbackPath, err := opt.listen()
	if err != nil {
		return nil, err
	}
	defer ln.Close()

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {opt.ClientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if opt.Scopes != "" {
		params.Set("scope", opt.Scopes)
	}
	if opt.LoginHint != "" {
		params.Set("login_hint", opt.LoginHint)
	}
	authURL := opt.AuthorizeURL + "?" + params.Encode()

	codeCh := make(chan callbackResult, 1)
	srv := &http.Server{Handler: callbackHandler(callbackPath, state, codeCh)}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	fmt.Fprintln(os.Stderr, "keydris: opening your browser to sign in...")
	if opt.NoBrowser {
		fmt.Fprintf(os.Stderr, "keydris: open this URL to continue:\n  %s\n", authURL)
	} else if err := opt.Open(authURL); err != nil {
		fmt.Fprintf(os.Stderr, "keydris: could not open a browser (%v); open this URL manually:\n  %s\n", err, authURL)
	}

	var code string
	select {
	case res := <-codeCh:
		if res.err != nil {
			return nil, res.err
		}
		code = res.code
	case <-time.After(opt.Timeout):
		return nil, fmt.Errorf("timed out waiting for browser login after %s", opt.Timeout)
	}

	tokens, err := exchangeCode(opt, code, verifier, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	// CSR subject is advisory; the control plane sets the real identity from the
	// verified token, so an empty hint here is fine.
	csrPEM, err := makeCSR(key, tokens.email)
	if err != nil {
		return nil, fmt.Errorf("build CSR: %w", err)
	}

	// Present the ID token to the control plane when available (Cognito), else
	// the access token (mock IdP). The control plane verifies it before signing.
	bearer := tokens.idToken
	if bearer == "" {
		bearer = tokens.accessToken
	}
	signed, err := signCSR(opt.ControlURL, bearer, csrPEM)
	if err != nil {
		return nil, fmt.Errorf("sign certificate: %w", err)
	}

	id := &Identity{
		Email:      signed.Email,
		Subject:    signed.Subject,
		SPIFFEID:   signed.SPIFFEID,
		NotAfter:   signed.NotAfter,
		ControlURL: opt.ControlURL,
		LoggedInAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := store(opt.IdentityDir, id, key, signed); err != nil {
		return nil, fmt.Errorf("store identity: %w", err)
	}
	return id, nil
}

type callbackResult struct {
	code string
	err  error
}

// applyDefaults fills unset Options with the built-in mock-IdP values so the
// flow works offline and the existing tests keep passing.
func (o *Options) applyDefaults() {
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Minute
	}
	if o.ClientID == "" {
		o.ClientID = defaultClientID
	}
	if o.AuthorizeURL == "" {
		o.AuthorizeURL = strings.TrimRight(o.ControlURL, "/") + "/oauth/authorize"
	}
	if o.TokenURL == "" {
		o.TokenURL = strings.TrimRight(o.ControlURL, "/") + "/oauth/token"
	}
}

// listen binds the loopback callback. With a fixed RedirectURL (Cognito) it
// binds that exact host:port and path; otherwise it picks an ephemeral port and
// derives the redirect URI from it (mock IdP).
func (o *Options) listen() (ln net.Listener, redirectURI, path string, err error) {
	if o.RedirectURL == "" {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, "", "", fmt.Errorf("start loopback listener: %w", err)
		}
		return l, fmt.Sprintf("http://%s/callback", l.Addr().String()), "/callback", nil
	}
	u, err := url.Parse(o.RedirectURL)
	if err != nil {
		return nil, "", "", fmt.Errorf("bad redirect URL %q: %w", o.RedirectURL, err)
	}
	path = u.Path
	if path == "" {
		path = "/"
	}
	l, err := net.Listen("tcp", u.Host)
	if err != nil {
		return nil, "", "", fmt.Errorf("bind callback %s (is the port free, and registered in the provider?): %w", u.Host, err)
	}
	return l, o.RedirectURL, path, nil
}

// callbackHandler serves the loopback redirect target, validating state and
// handing the code back over ch. It responds with a small page so the browser
// tab shows a friendly result.
func callbackHandler(path, wantState string, ch chan<- callbackResult) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			msg := e
			if d := q.Get("error_description"); d != "" {
				msg += ": " + d
			}
			finish(w, ch, callbackResult{err: fmt.Errorf("authorization denied: %s", msg)}, false)
			return
		}
		if q.Get("state") != wantState {
			finish(w, ch, callbackResult{err: fmt.Errorf("state mismatch (possible CSRF)")}, false)
			return
		}
		code := q.Get("code")
		if code == "" {
			finish(w, ch, callbackResult{err: fmt.Errorf("no authorization code in callback")}, false)
			return
		}
		finish(w, ch, callbackResult{code: code}, true)
	})
	return mux
}

func finish(w http.ResponseWriter, ch chan<- callbackResult, res callbackResult, ok bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ok {
		_, _ = io.WriteString(w, resultPage("Signed in", "You can close this tab and return to the terminal."))
	} else {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, resultPage("Login failed", "Return to the terminal for details."))
	}
	select {
	case ch <- res:
	default:
	}
}

// tokenSet is the subset of an OAuth token response we use.
type tokenSet struct {
	idToken     string
	accessToken string
	email       string // mock IdP only; Cognito carries email inside the ID token
}

func exchangeCode(opt Options, code, verifier, redirectURI string) (*tokenSet, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
		"client_id":     {opt.ClientID},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, opt.TokenURL, bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// Confidential client: Cognito requires the secret via HTTP Basic on the
	// token endpoint. PKCE still applies; the secret is in addition to it.
	if opt.ClientSecret != "" {
		req.SetBasicAuth(opt.ClientID, opt.ClientSecret)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		Email       string `json:"email"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || (out.IDToken == "" && out.AccessToken == "") {
		msg := out.Error
		if out.ErrorDesc != "" {
			msg += ": " + out.ErrorDesc
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("token endpoint: %s", msg)
	}
	return &tokenSet{idToken: out.IDToken, accessToken: out.AccessToken, email: out.Email}, nil
}

func makeCSR(key *ecdsa.PrivateKey, email string) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: email},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// signResponse mirrors authn.SignResponse without importing the server package.
type signResponse struct {
	Certificate string `json:"certificate"`
	CACert      string `json:"ca_cert"`
	SPIFFEID    string `json:"spiffe_id"`
	Subject     string `json:"subject"`
	Email       string `json:"email"`
	NotAfter    string `json:"not_after"`
}

func signCSR(controlURL, token string, csrPEM []byte) (*signResponse, error) {
	body, _ := json.Marshal(map[string]string{"csr": string(csrPEM)})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, controlURL+"/identity/sign", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("sign endpoint %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	var out signResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- PKCE helpers ---

func randURL(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func resultPage(title, msg string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>` + title + `</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0d12;color:#e7e9ee;display:flex;
min-height:100vh;align-items:center;justify-content:center;margin:0}
.b{text-align:center}.b h1{font-size:20px;margin:0 0 8px}.b p{color:#9aa3b2}</style></head>
<body><div class="b"><h1>` + title + `</h1><p>` + msg + `</p></div></body></html>`
}
