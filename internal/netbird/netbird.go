// Package netbird checks whether the NetBird client is currently connected,
// via its own CLI health check.
package netbird

import "os/exec"

// Connected reports whether NetBird is up and connected. bin overrides which
// netbird executable to use; if empty, common install locations are tried
// since a LaunchDaemon's PATH usually excludes /usr/local/bin.
func Connected(bin string) bool {
	return exec.Command(resolveBin(bin), "status", "-C", "ready").Run() == nil
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
