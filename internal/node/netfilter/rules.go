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
	"strings"
	"sync"
)

const chain = "KEYDRIS"

var (
	routeLocalnetMu       sync.Mutex
	routeLocalnetOriginal string
	routeLocalnetChanged  bool
)

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

	// Best-effort: permit REDIRECT of loopback-destined traffic, remembering the
	// old value so clean shutdown does not leave a host-wide sysctl changed.
	enableRouteLocalnet()

	if err := iptables("-t", "nat", "-N", chain); err != nil {
		restoreRouteLocalnet()
		return err
	}
	// Exempt the proxy's own egress so proxy->backend is not re-redirected.
	if err := iptables("-t", "nat", "-A", chain,
		"-m", "owner", "--uid-owner", strconv.Itoa(proxyUID), "-j", "RETURN"); err != nil {
		_ = Down(proxyPort, backendPort, proxyUID)
		return err
	}
	// Redirect everything else destined for the backend into the proxy.
	if err := iptables("-t", "nat", "-A", chain,
		"-p", "tcp", "--dport", strconv.Itoa(backendPort),
		"-j", "REDIRECT", "--to-ports", strconv.Itoa(proxyPort)); err != nil {
		_ = Down(proxyPort, backendPort, proxyUID)
		return err
	}
	// Hook the chain into OUTPUT, scoped to the backend port.
	if err := iptables("-t", "nat", "-A", "OUTPUT",
		"-p", "tcp", "--dport", strconv.Itoa(backendPort), "-j", chain); err != nil {
		_ = Down(proxyPort, backendPort, proxyUID)
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
	restoreRouteLocalnet()
	return nil
}

func enableRouteLocalnet() {
	routeLocalnetMu.Lock()
	defer routeLocalnetMu.Unlock()
	if routeLocalnetChanged {
		return
	}
	out, err := exec.Command("sysctl", "-n", "net.ipv4.conf.all.route_localnet").Output()
	if err != nil {
		return
	}
	original := strings.TrimSpace(string(out))
	if original == "" || original == "1" {
		return
	}
	if exec.Command("sysctl", "-w", "net.ipv4.conf.all.route_localnet=1").Run() == nil {
		routeLocalnetOriginal = original
		routeLocalnetChanged = true
	}
}

func restoreRouteLocalnet() {
	routeLocalnetMu.Lock()
	defer routeLocalnetMu.Unlock()
	if !routeLocalnetChanged {
		return
	}
	if exec.Command("sysctl", "-w", "net.ipv4.conf.all.route_localnet="+routeLocalnetOriginal).Run() == nil {
		routeLocalnetChanged = false
		routeLocalnetOriginal = ""
	}
}
