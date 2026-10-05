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
// reach dest, a single IPv4 address or an IPv4 CIDR range. For a range with
// no route of its own this is the closest covering route (e.g. default).
func RouteInfo(dest string) (gateway, iface string, err error) {
	out, err := routeCmd("get", dest)
	if err != nil {
		return "", "", err
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

// AddRoute installs a route for dest (a host route for an address, a network
// route for a CIDR range) via gateway.
func AddRoute(dest, gateway string) error {
	_, err := routeCmd("add", dest, gateway)
	return err
}

// AddInterfaceRoute installs a route for dest bound directly to an interface
// (no gateway), matching how NetBird installs its own routes onto its
// point-to-point utun interface.
func AddInterfaceRoute(dest, iface string) error {
	_, err := routeCmd("add", dest, "-interface", iface)
	return err
}

func DeleteRoute(dest string) error {
	out, err := routeCmd("delete", dest)
	if err != nil && !strings.Contains(string(out), "not in table") {
		return err
	}
	return nil
}

// routeCmd runs "route -n <action>" for dest, selecting it with -net for a
// CIDR range and -host for a single address.
func routeCmd(action, dest string, extra ...string) ([]byte, error) {
	kind := "-host"
	if strings.Contains(dest, "/") {
		kind = "-net"
	}
	args := append([]string{"-n", action, kind, dest}, extra...)
	out, err := exec.Command("route", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("route %s: %w: %s", strings.Join(args[1:], " "), err, out)
	}
	return out, nil
}
