package routing

import (
	"fmt"
	"os/exec"
	"strings"
)

func DefaultGateway() (gateway string, iface string, err error) {
	out, err := exec.Command("route", "-n", "get", "default").CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("route get default: %w: %s", err, out)
	}
	return parseGatewayAndInterface(string(out))
}

// RouteInfo reports the gateway and interface the kernel currently uses to
// reach ip.
func RouteInfo(ip string) (gateway, iface string, err error) {
	out, err := exec.Command("route", "-n", "get", "-host", ip).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("route get %s: %w: %s", ip, err, out)
	}
	return parseGatewayAndInterface(string(out))
}

func parseGatewayAndInterface(routeGetOutput string) (gateway, iface string, err error) {
	for _, line := range strings.Split(routeGetOutput, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "gateway:"):
			gateway = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
		case strings.HasPrefix(line, "interface:"):
			iface = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	if iface == "" {
		return "", "", fmt.Errorf("could not parse interface from route output:\n%s", routeGetOutput)
	}
	return gateway, iface, nil
}

func AddHostRoute(ip, gateway string) error {
	out, err := exec.Command("route", "-n", "add", "-host", ip, gateway).CombinedOutput()
	if err != nil {
		return fmt.Errorf("route add -host %s %s: %w: %s", ip, gateway, err, out)
	}
	return nil
}

// AddInterfaceRoute installs a /32 host route for ip bound directly to an
// interface (no gateway), matching how NetBird installs its own routes onto
// its point-to-point utun interface.
func AddInterfaceRoute(ip, iface string) error {
	out, err := exec.Command("route", "-n", "add", "-host", ip, "-interface", iface).CombinedOutput()
	if err != nil {
		return fmt.Errorf("route add -host %s -interface %s: %w: %s", ip, iface, err, out)
	}
	return nil
}

func DeleteHostRoute(ip string) error {
	out, err := exec.Command("route", "-n", "delete", "-host", ip).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "not in table") {
		return fmt.Errorf("route delete -host %s: %w: %s", ip, err, out)
	}
	return nil
}
