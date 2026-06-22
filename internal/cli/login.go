package cli

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nocaplabs/keydris-cli/internal/config"
	"github.com/nocaplabs/keydris-cli/internal/node/login"
)

// runLogin implements `keydris login`: a browser-based OAuth (PKCE) sign-in
// against the control plane that results in a locally stored client
// certificate. The private key is generated on this machine and never leaves
// it; the control plane only signs the CSR. The daemon later presents this
// certificate over mTLS when calling the control plane.
func runLogin(args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	email := fs.String("email", defaultLoginHint(), "suggested identity to pre-fill on the consent page")
	noBrowser := fs.Bool("no-browser", false, "print the sign-in URL instead of opening a browser")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	cfg := config.Load()
	opt := login.Options{
		ControlURL:  cfg.ControlURL,
		IdentityDir: cfg.IdentityDir,
		LoginHint:   *email,
		NoBrowser:   *noBrowser,
		Timeout:     3 * time.Minute,
	}
	if cfg.LoginUsesExternalIDP() {
		// External provider (e.g. AWS Cognito): use its endpoints and the fixed,
		// pre-registered loopback callback.
		opt.AuthorizeURL = cfg.OAuthAuthorizeURL
		opt.TokenURL = cfg.OAuthTokenURL
		opt.ClientID = cfg.OAuthClientID
		opt.ClientSecret = cfg.OAuthClientSecret
		opt.Scopes = cfg.OAuthScopes
		opt.RedirectURL = cfg.OAuthRedirectURL
		opt.LoginHint = "" // the provider runs its own credential prompt
		fmt.Fprintf(os.Stderr, "keydris: signing in via %s\n", cfg.OAuthAuthorizeURL)
	}

	id, err := login.Run(opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris login: %v\n", err)
		return 1
	}

	fmt.Printf("keydris: signed in as %s\n", id.Email)
	fmt.Printf("  identity:   %s\n", id.SPIFFEID)
	fmt.Printf("  cert until: %s\n", id.NotAfter)
	fmt.Printf("  stored in:  %s\n", cfg.IdentityDir)
	return 0
}

// runWhoami implements `keydris whoami`: report the locally stored identity.
func runWhoami(args []string) int {
	cfg := config.Load()
	id, err := login.Load(cfg.IdentityDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris whoami: %v\n", err)
		return 1
	}
	status := "valid"
	if id.Expired() {
		status = "EXPIRED (run `keydris login`)"
	}
	fmt.Printf("email:      %s\n", id.Email)
	fmt.Printf("identity:   %s\n", id.SPIFFEID)
	fmt.Printf("subject:    %s\n", id.Subject)
	fmt.Printf("control:    %s\n", id.ControlURL)
	fmt.Printf("logged in:  %s\n", id.LoggedInAt)
	fmt.Printf("cert until: %s (%s)\n", id.NotAfter, status)
	return 0
}

// runLogout implements `keydris logout`: remove the stored identity material.
func runLogout(args []string) int {
	cfg := config.Load()
	if err := login.Logout(cfg.IdentityDir); err != nil {
		fmt.Fprintf(os.Stderr, "keydris logout: %v\n", err)
		return 1
	}
	fmt.Println("keydris: logged out (local identity removed)")
	return 0
}

// defaultLoginHint suggests "<user>@<host>" to pre-fill the consent page in the
// POC. A real IdP ignores this and runs its own credential prompt.
func defaultLoginHint() string {
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	if user == "" {
		return ""
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "keydris.local"
	}
	return user + "@" + host
}
