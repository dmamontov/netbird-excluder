// Package netbird checks whether the NetBird client is currently connected
// and which local network interface it's using, via its own CLI.
package netbird

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// Connected reports whether NetBird is up and connected. bin overrides which
// netbird executable to use; if empty, common install locations are tried
// since a LaunchDaemon's PATH usually excludes /usr/local/bin.
func Connected(bin string) bool {
	return exec.Command(resolveBin(bin), "status", "-C", "ready").Run() == nil
}

// InterfaceName returns the name of the local network interface NetBird is
// using (e.g. "utun100"), found by asking NetBird for its own overlay IP and
// looking up which interface currently holds it. This is more reliable than
// guessing from a name like "utun*", since other software (other VPNs,
// iCloud Private Relay, ...) creates utun interfaces too.
func InterfaceName(bin string) (string, error) {
	out, err := exec.Command(resolveBin(bin), "status", "-4").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("netbird status -4: %w: %s", err, out)
	}
	ip := strings.TrimSpace(string(out))

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if ok && ipNet.IP.String() == ip {
				return iface.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no local interface holds NetBird IP %s", ip)
}

func resolveBin(bin string) string {
	if bin != "" {
		return bin
	}
	for _, candidate := range []string{"netbird", "/usr/local/bin/netbird", "/opt/homebrew/bin/netbird"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return "netbird"
}
