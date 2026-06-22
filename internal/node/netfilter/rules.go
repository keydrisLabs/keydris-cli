// Package netfilter manages the scoped iptables rules that transparently
// redirect agent egress into the Keydris proxy.
//
// Rules live in a dedicated nat chain (KEYDRIS) so teardown is clean and stale
// rules from a previous run are removed on every Up().
package netfilter

import (
	"fmt"
	"os/exec"
	"strconv"
)

const chain = "KEYDRIS"

func iptables(args ...string) error {
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %v: %v (%s)", args, err, out)
	}
	return nil
}

func iptablesIgnore(args ...string) { _ = iptables(args...) }

// Up installs the redirect rules: TCP traffic to backendPort is redirected to
// proxyPort, except traffic originating from proxyUID (the proxy's own egress),
// which would otherwise loop back into the proxy forever.
func Up(proxyPort, backendPort, proxyUID int) error {
	// Always start from a clean slate.
	Down(proxyPort, backendPort, proxyUID)

	// Best-effort: permit REDIRECT of loopback-destined traffic. Ignored on
	// kernels/configs where it is not applicable.
	_ = exec.Command("sysctl", "-w", "net.ipv4.conf.all.route_localnet=1").Run()

	if err := iptables("-t", "nat", "-N", chain); err != nil {
		return err
	}
	// Exempt the proxy's own egress so proxy->backend is not re-redirected.
	if err := iptables("-t", "nat", "-A", chain,
		"-m", "owner", "--uid-owner", strconv.Itoa(proxyUID), "-j", "RETURN"); err != nil {
		return err
	}
	// Redirect everything else destined for the backend into the proxy.
	if err := iptables("-t", "nat", "-A", chain,
		"-p", "tcp", "--dport", strconv.Itoa(backendPort),
		"-j", "REDIRECT", "--to-ports", strconv.Itoa(proxyPort)); err != nil {
		return err
	}
	// Hook the chain into OUTPUT, scoped to the backend port.
	if err := iptables("-t", "nat", "-A", "OUTPUT",
		"-p", "tcp", "--dport", strconv.Itoa(backendPort), "-j", chain); err != nil {
		return err
	}
	return nil
}

// Down removes the rules and the chain. Safe to call repeatedly; errors from
// already-absent rules are ignored.
func Down(_ /*proxyPort*/, backendPort, _ /*proxyUID*/ int) error {
	iptablesIgnore("-t", "nat", "-D", "OUTPUT",
		"-p", "tcp", "--dport", strconv.Itoa(backendPort), "-j", chain)
	iptablesIgnore("-t", "nat", "-F", chain)
	iptablesIgnore("-t", "nat", "-X", chain)
	return nil
}
