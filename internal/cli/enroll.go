package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/nocaplabs/keydris-cli/internal/config"
	"github.com/nocaplabs/keydris-cli/internal/node/enroll"
)

// runEnroll implements `keydris enroll` (root): exchange an enrollment token
// for a persistent node credential. This is the legacy transparent/Linux node
// onboarding path; the user-facing sign-in is now `keydris login` (browser +
// client certificate, see login.go). The token can be supplied with --token.
func runEnroll(args []string) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	token := fs.String("token", "", "enrollment token (a fresh one is generated if omitted)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "keydris enroll: must run as root (node credential is root-owned); try: sudo keydris enroll")
		return 1
	}

	cfg := config.Load()
	tok := *token
	if tok == "" {
		// Fall back to a token previously saved on disk, else mint one so this
		// command works standalone (the legacy node path does not gate on the IdP).
		if saved, err := enroll.LoadToken(cfg.DataDir); err == nil {
			tok = saved
		}
	}
	cred, err := enroll.Enroll(cfg.DataDir, tok)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keydris enroll: %v\n", err)
		return 1
	}
	fmt.Printf("keydris: node enrolled as %s\n", cred.NodeID)
	return 0
}
