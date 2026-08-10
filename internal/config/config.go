// Package config provides layered configuration for the Keydris POC binaries.
//
// Phase 1 keeps this intentionally small: values come from the environment
// (optionally seeded from a local .env file), with sane defaults so the demo
// runs with zero configuration. The same Config is shared by the control plane,
// the mock backend, and the node daemon so the injected credential always
// matches what the backend expects.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Channel is stamped as "stable" only for tag-triggered releases.
// It affects client-facing fallback URLs (ControlURL, etc.) only.
// Local control-plane/backend addresses and DataDir always remain
// pinned to 127.0.0.1, regardless of Channel.
var Channel = ""

// Config is the full set of knobs the POC needs. Not every field is used by
// every binary, but sharing one struct keeps the credential and ports in sync.
type Config struct {
	// ControlAddr is the listen address for keydris-control (issuer + broker).
	ControlAddr string
	// ControlURL is the base URL the proxy uses to reach the control plane.
	ControlURL string
	// ControlMTLSAddr is the listen address for the mTLS-only data-plane
	// endpoints (/agent/authorize, /agent/authorize/issue,
	// /agent/authorize/{ulid}/revoke). The login/bootstrap endpoints stay on the
	// plain-HTTP ControlAddr.
	ControlMTLSAddr string
	// ControlMTLSURL is the base URL the daemon/hooks use to reach those mTLS
	// endpoints, presenting the client certificate `keydris login` stored.
	ControlMTLSURL string
	// MTLSServerCA optionally pins an extra CA to trust when verifying the mTLS
	// control-plane server certificate. In production the mTLS endpoint sits
	// behind an ALB presenting a public TLS cert (verified against the system
	// roots), so this is empty. Set it only for a local/self-signed control
	// plane. NOTE: this is the *server* CA — not the client `ca.crt` from
	// /identity/sign, which signs client certs and must not verify the server.
	MTLSServerCA string
	// ProxyPort is the transparent proxy listen port (iptables REDIRECT target),
	// or the proxy-env listen port in the fallback mode.
	ProxyPort int
	// DataPlane selects the interception mode: "sandbox" (Claude Code custom
	// proxy, the v2 default), "transparent" (Linux iptables/eBPF), or "proxyenv"
	// (cross-platform HTTP_PROXY fallback).
	DataPlane string
	// BackendAddr is the mock-backend listen address (host:port).
	BackendAddr string
	// BackendPort is the protected backend port that egress is redirected for.
	BackendPort int
	// BackendTLS makes mock-backend serve HTTPS (self-signed) so the sandbox
	// proxy's CONNECT + TLS-termination (MITM) path can be exercised end-to-end.
	BackendTLS bool
	// ProxyUID is the uid the daemon/proxy runs as; its own egress is exempted
	// from redirection to avoid an infinite loop. The daemon runs as root in the
	// POC, so this is 0.
	ProxyUID int
	// CredHeader is the request header the broker injects the minted access
	// token into (default Authorization, as a Bearer token). The token itself
	// is minted per-allow by the control plane, never a static shared secret.
	CredHeader string
	// AccessTTLSeconds is the lifetime of the per-allow upstream access token.
	// Kept short (seconds) for least privilege — a leaked token expires fast.
	AccessTTLSeconds int
	// BackendAudience is the audience (aud) the mock-backend requires in the
	// access token: a token minted for another upstream is rejected.
	BackendAudience string

	// --- Identity core (control plane) ---

	// TrustDomain is the SPIFFE trust domain (spiffe://<td>/agent/<bp>/<ulid>).
	TrustDomain string
	// DataDir holds control-plane state (signing key, evidence ledger).
	DataDir string
	// SigningKeyPath is the Ed25519 SVID signing key (created if missing).
	SigningKeyPath string
	// LedgerPath is the append-only hash-chained evidence ledger (JSONL).
	LedgerPath string
	// GrantsSeedPath seeds the grant store at startup.
	GrantsSeedPath string
	// SVIDTTLSeconds is the default per-session SVID lifetime.
	SVIDTTLSeconds int

	// --- Session hooks (Phase 4) ---

	// Blueprint is the configured blueprint (highest non-flag precedence).
	Blueprint string
	// SessionSocket is the daemon's local registration socket (cgroup<->SVID).
	SessionSocket string
	// SessionAuthFile contains the local bearer secret shared by the CLI and
	// daemon for authenticated session-registry messages.
	SessionAuthFile string
	// PolicyID is the governance policy this node enforces, set by
	// `keydris init claude-code <policy-id>` and persisted under DataDir. The
	// daemon sends it to the broker on every authorize so the decision resolves
	// against the right policy.
	PolicyID string
	// AgentID is the explicit control-plane agent identity configured by
	// `keydris init <target> <agent-id>`. New runtime sessions are always minted
	// for this UUID; PolicyID remains only for legacy authorize payloads.
	AgentID string

	// --- Sandbox proxy (v2: Claude Code custom-proxy integration) ---

	// CAPath / CAKeyPath persist the Keydris CA used to terminate TLS in the
	// sandbox proxy. The CA must be stable across restarts once installed in the
	// sandbox/OS trust store.
	CAPath       string
	CAKeyPath    string
	CABundlePath string
	// ClaudeSettingsPath is the Claude Code settings file `keydris init` writes
	// the sandbox block + CA env into (default ~/.claude/settings.json).
	ClaudeSettingsPath string
	// CodexHooksPath is the Codex hooks file `keydris init codex` writes the
	// command-gating hooks into (default $CODEX_HOME/hooks.json, with
	// CODEX_HOME defaulting to ~/.codex).
	CodexHooksPath string
	// HTTPProxyPort is the port the Claude Code sandbox routes egress to
	// (sandbox.network.httpProxyPort). Defaults to ProxyPort.
	HTTPProxyPort int
	// AllowedDomains seeds sandbox.allowedDomains so the agent's egress (and the
	// model endpoint) is permitted through the sandbox to the Keydris proxy.
	AllowedDomains []string
	// ManagedMode and ManagedDestinations control which exact origins are
	// authorized and credential-injected. Mode "all" preserves the historical
	// behavior; "selected" passes every unlisted destination through unchanged.
	ManagedMode         string
	ManagedDestinations []string
	ManagedScopeError   error
	// AllowSoleFallback lets the sandbox proxy attribute a request that carries
	// no per-session token to the sole registered session. Off by default: a
	// request must present its token, else it is unattributed (fail-closed). This
	// closes the "any local process reaching the proxy borrows the lone session"
	// gap; flip it on only to restore the single-session convenience.
	AllowSoleFallback bool
	// PeerVerify controls connecting-process verification in the sandbox proxy:
	// "off", "warn" (default — log mismatches), or "enforce" (reject a connection
	// whose process is not in the session's tree). Effective on Linux; a no-op on
	// platforms that cannot resolve the peer (see docs/attribution.md, Tier 3).
	PeerVerify string

	// --- Browser login (`keydris login`) ---

	// ClientCAPath / ClientCAKeyPath are the control-plane enrollment CA that
	// signs the per-user client certificates `keydris login` issues. Distinct
	// from the proxy MITM CA (CAPath) on purpose: this one authenticates the
	// daemon to the control plane.
	ClientCAPath    string
	ClientCAKeyPath string
	// IdentityDir holds the user's logged-in identity on the node side: the
	// locally generated private key, the signed client cert, the pinned CA, and
	// whoami metadata. `keydris login` writes it; the daemon reads it.
	IdentityDir string
	// IdentityTTLSeconds is the lifetime of an issued client certificate.
	IdentityTTLSeconds int

	// --- External OIDC provider for `keydris login` (e.g. AWS Cognito) ---
	//
	// When OAuthAuthorizeURL is set, `keydris login` runs the browser flow
	// against this provider instead of the built-in mock IdP, and the control
	// plane verifies the provider's ID token (against OIDCIssuer's JWKS) before
	// signing the client certificate. When unset, the built-in mock IdP is used.

	// OIDCIssuer is the token issuer used to verify the ID token and locate the
	// JWKS (Cognito: https://cognito-idp.<region>.amazonaws.com/<userPoolId>).
	OIDCIssuer string
	// OAuthAuthorizeURL / OAuthTokenURL are the provider's OAuth endpoints. For
	// Cognito they are derived from KEYDRIS_COGNITO_DOMAIN when not set
	// explicitly (https://<domain>/oauth2/authorize and .../oauth2/token).
	OAuthAuthorizeURL string
	OAuthTokenURL     string
	// OAuthClientID is the provider app-client ID (also the expected ID-token
	// audience).
	OAuthClientID string
	// OAuthClientSecret is the app-client secret for a confidential client. When
	// set, the token exchange authenticates with HTTP Basic
	// (base64(client_id:client_secret)); leave empty for a public client. Note a
	// secret embedded in a CLI is not truly confidential — PKCE is the real
	// protection here; the secret is sent only because Cognito requires it.
	OAuthClientSecret string
	// OAuthScopes is the space-separated scope set; must include the scopes that
	// yield an email claim (default "openid email").
	OAuthScopes string
	// OAuthRedirectURL is the fixed loopback callback the provider redirects to.
	// Cognito requires an exact match, so this must be registered verbatim in
	// the app client's allowed callback URLs.
	OAuthRedirectURL string
}

// Load reads configuration from the environment and trusted user config.
// Project-local files are loaded only when KEYDRIS_TRUST_PROJECT_CONFIG=1 was
// explicitly present in the process environment.
func Load() *Config {
	loadLayeredFiles()
	dataDir := env("KEYDRIS_DATA_DIR", defaultDataDir())
	authorizeURL, tokenURL := cognitoEndpoints()
	managed, managedErr := loadManagedScope(dataDir)
	if value, ok := os.LookupEnv("KEYDRIS_MANAGED_MODE"); ok {
		managed.Mode = strings.TrimSpace(value)
	}
	if _, ok := os.LookupEnv("KEYDRIS_MANAGED_DESTINATIONS"); ok {
		managed.Destinations = envList("KEYDRIS_MANAGED_DESTINATIONS")
	}
	return &Config{
		ControlAddr:     env("KEYDRIS_CONTROL_ADDR", "127.0.0.1:8081"),
		ControlURL:      envOrChannelDefault("KEYDRIS_CONTROL_URL", "http://127.0.0.1:8081", "https://api.keydris.com"),
		ControlMTLSAddr: env("KEYDRIS_CONTROL_MTLS_ADDR", "127.0.0.1:8443"),
		ControlMTLSURL:  envOrChannelDefault("KEYDRIS_CONTROL_MTLS_URL", "https://127.0.0.1:8443", "https://api.keydris.com:8443"),
		MTLSServerCA:    env("KEYDRIS_MTLS_SERVER_CA", ""),
		ProxyPort:       envInt("KEYDRIS_PROXY_PORT", 15001),
		// Sandbox (the Claude Code custom proxy) is the default plane, so users
		// never need to set KEYDRIS_DATAPLANE. Override to "transparent"
		// (Linux/root) or "proxyenv" only for those paths.
		DataPlane:        env("KEYDRIS_DATAPLANE", "sandbox"),
		BackendAddr:      env("KEYDRIS_BACKEND_ADDR", "127.0.0.1:8080"),
		BackendPort:      envInt("KEYDRIS_BACKEND_PORT", 8080),
		BackendTLS:       env("KEYDRIS_BACKEND_TLS", "") != "",
		ProxyUID:         envInt("KEYDRIS_PROXY_UID", 0),
		CredHeader:       env("KEYDRIS_CRED_HEADER", "Authorization"),
		AccessTTLSeconds: envInt("KEYDRIS_ACCESS_TTL_SECONDS", 60),
		BackendAudience:  env("KEYDRIS_BACKEND_AUDIENCE", "mock-backend"),

		TrustDomain:    env("KEYDRIS_TRUST_DOMAIN", "keydris.local"),
		DataDir:        dataDir,
		SigningKeyPath: env("KEYDRIS_SIGNING_KEY", filepath.Join(dataDir, "signing.key")),
		LedgerPath:     env("KEYDRIS_LEDGER_PATH", filepath.Join(dataDir, "evidence.jsonl")),
		GrantsSeedPath: env("KEYDRIS_GRANTS_SEED", "deploy/seed/grants.seed.json"),
		SVIDTTLSeconds: envInt("KEYDRIS_SVID_TTL_SECONDS", 900),

		Blueprint:       env("KEYDRIS_BLUEPRINT", ""),
		SessionSocket:   env("KEYDRIS_SESSION_SOCKET", filepath.Join(dataDir, "registry.sock")),
		SessionAuthFile: env("KEYDRIS_SESSION_AUTH_FILE", filepath.Join(dataDir, "session.auth")),
		PolicyID:        env("KEYDRIS_POLICY_ID", readPolicyID(dataDir)),
		AgentID:         env("KEYDRIS_AGENT_ID", readAgentID(dataDir)),

		CAPath:              env("KEYDRIS_CA_PATH", filepath.Join(dataDir, "ca.crt")),
		CAKeyPath:           env("KEYDRIS_CA_KEY_PATH", filepath.Join(dataDir, "ca.key")),
		CABundlePath:        env("KEYDRIS_CA_BUNDLE_PATH", filepath.Join(dataDir, "ca-bundle.crt")),
		ClaudeSettingsPath:  env("KEYDRIS_CLAUDE_SETTINGS", defaultClaudeSettings()),
		CodexHooksPath:      env("KEYDRIS_CODEX_HOOKS", defaultCodexHooks()),
		HTTPProxyPort:       envInt("KEYDRIS_HTTP_PROXY_PORT", envInt("KEYDRIS_PROXY_PORT", 15001)),
		AllowedDomains:      envList("KEYDRIS_ALLOWED_DOMAINS"),
		ManagedMode:         managed.Mode,
		ManagedDestinations: managed.Destinations,
		ManagedScopeError:   managedErr,
		AllowSoleFallback:   env("KEYDRIS_ALLOW_SOLE_FALLBACK", "") != "",
		PeerVerify:          env("KEYDRIS_PEER_VERIFY", "warn"),

		ClientCAPath:       env("KEYDRIS_CLIENT_CA_PATH", filepath.Join(dataDir, "client-ca.crt")),
		ClientCAKeyPath:    env("KEYDRIS_CLIENT_CA_KEY_PATH", filepath.Join(dataDir, "client-ca.key")),
		IdentityDir:        env("KEYDRIS_IDENTITY_DIR", filepath.Join(dataDir, "identity")),
		IdentityTTLSeconds: envInt("KEYDRIS_IDENTITY_TTL_SECONDS", 43200),

		OIDCIssuer:        envOrChannelDefault("KEYDRIS_OIDC_ISSUER", "", "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_ft2ArrhS7"),
		OAuthAuthorizeURL: authorizeURL,
		OAuthTokenURL:     tokenURL,
		OAuthClientID:     envOrChannelDefault("KEYDRIS_OAUTH_CLIENT_ID", "", "6rll39d1l5hq1irjag9o4cu8m2"),
		OAuthClientSecret: env("KEYDRIS_OAUTH_CLIENT_SECRET", ""),
		OAuthScopes:       env("KEYDRIS_OAUTH_SCOPES", "openid email"),
		OAuthRedirectURL:  envOrChannelDefault("KEYDRIS_OAUTH_REDIRECT_URL", "http://localhost:3000/callback", "https://app.keydris.com/auth/callback"),
	}
}

// defaultDataDir keeps identity material and daemon state stable across working
// directories. Falling back to a relative path is reserved for environments
// where the operating system cannot resolve a home directory.
func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".keydris-data"
	}
	return filepath.Join(home, ".keydris-data")
}

// cognitoEndpoints derives the OAuth authorize/token endpoints, preferring
// explicit overrides and otherwise building them from a Cognito Hosted-UI
// domain (KEYDRIS_COGNITO_DOMAIN, e.g. myapp.auth.us-east-1.amazoncognito.com).
func cognitoEndpoints() (authorizeURL, tokenURL string) {
	authorizeURL = env("KEYDRIS_OAUTH_AUTHORIZE_URL", "")
	tokenURL = env("KEYDRIS_OAUTH_TOKEN_URL", "")
	domain := envOrChannelDefault("KEYDRIS_COGNITO_DOMAIN", "", "https://auth.keydris.com")
	if domain != "" {
		base := domain
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			base = "https://" + base
		}
		base = strings.TrimRight(base, "/")
		if authorizeURL == "" {
			authorizeURL = base + "/oauth2/authorize"
		}
		if tokenURL == "" {
			tokenURL = base + "/oauth2/token"
		}
	}
	return authorizeURL, tokenURL
}

// LoginUsesExternalIDP reports whether `keydris login` should run against an
// external OIDC provider (vs the built-in mock IdP).
func (c *Config) LoginUsesExternalIDP() bool {
	return c.OAuthAuthorizeURL != "" && c.OAuthTokenURL != "" && c.OAuthClientID != ""
}

// defaultClaudeSettings returns ~/.claude/settings.json, falling back to a
// relative path if the home directory cannot be determined.
func defaultClaudeSettings() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".claude/settings.json"
	}
	return filepath.Join(home, ".claude", "settings.json")
}

func defaultCodexHooks() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		if codexHome == "~" || strings.HasPrefix(codexHome, "~/") || strings.HasPrefix(codexHome, `~\`) {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				codexHome = filepath.Join(home, strings.TrimLeft(codexHome[1:], `/\`))
			}
		}
		return filepath.Join(codexHome, "hooks.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".codex/hooks.json"
	}
	return filepath.Join(home, ".codex", "hooks.json")
}

// envList parses a comma-separated environment variable into a trimmed slice.
func envList(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ResolveBlueprint applies blueprint precedence: an explicit run/hook flag wins,
// then KEYDRIS_BLUEPRINT, then the policy id set by `keydris init claude-code
// <policy-id>`, then the built-in default. This is the single source of truth
// for which blueprint a session binds to — i.e. the agent segment of the SVID's
// SPIFFE ID (spiffe://<td>/agent/<blueprint>/<ulid>), which the broker authorizes
// against grants for that blueprint.
func (c *Config) ResolveBlueprint(flag string) string {
	switch {
	case flag != "":
		return flag
	case c.Blueprint != "":
		return c.Blueprint
	case c.PolicyID != "":
		return c.PolicyID
	default:
		return "repo-tools"
	}
}

// ResolveAgent selects the control-plane agent UUID. The legacy blueprint and
// policy values are accepted only as migration fallbacks for existing installs.
func (c *Config) ResolveAgent(flag string) string {
	switch {
	case flag != "":
		return flag
	case c.AgentID != "":
		return c.AgentID
	case c.Blueprint != "":
		return c.Blueprint
	case c.PolicyID != "":
		return c.PolicyID
	default:
		return ""
	}
}

func agentIDPath(dataDir string) string { return filepath.Join(dataDir, "agent-id") }

func readAgentID(dataDir string) string {
	b, err := os.ReadFile(agentIDPath(dataDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SaveAgentID persists the explicit agent identity for runtime session minting.
func SaveAgentID(dataDir, id string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dataDir, 0o700)
	path := agentIDPath(dataDir)
	if err := os.WriteFile(path, []byte(strings.TrimSpace(id)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func RemoveAgentID(dataDir string) error {
	if err := os.Remove(agentIDPath(dataDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// policyIDPath is where `keydris init` persists the active policy id.
func policyIDPath(dataDir string) string { return filepath.Join(dataDir, "policy-id") }

// readPolicyID returns the persisted policy id, or "" if none is set yet.
func readPolicyID(dataDir string) string {
	b, err := os.ReadFile(policyIDPath(dataDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SavePolicyID persists the policy id under the data dir so later `keydris proxy
// up` / `keydris status` pick it up without re-passing it.
func SavePolicyID(dataDir, id string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dataDir, 0o700)
	path := policyIDPath(dataDir)
	if err := os.WriteFile(path, []byte(strings.TrimSpace(id)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// RemovePolicyID clears the persisted policy id (used by `keydris deinit`). A
// missing file is not an error.
func RemovePolicyID(dataDir string) error {
	if err := os.Remove(policyIDPath(dataDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envOrChannelDefault is like env, but substitutes stableDefault for the
// fallback when this binary was built for the stable channel (see Channel).
func envOrChannelDefault(key, pocDefault, stableDefault string) string {
	if Channel == "stable" {
		pocDefault = stableDefault
	}
	return env(key, pocDefault)
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// loadDotEnv reads simple KEY=VALUE lines from path and sets them in the
// environment if not already present. Missing files are ignored.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
}
