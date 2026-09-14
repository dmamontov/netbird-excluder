package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"netbird-excluder/internal/config"
	"netbird-excluder/internal/enforcer"
	"netbird-excluder/internal/netbird"
	"netbird-excluder/internal/resolver"
	"netbird-excluder/internal/routing"
	"netbird-excluder/internal/service"
)

const defaultLabel = "local.netbird-excluder"
const defaultBinPath = "/usr/local/bin/netbird-excluder"

func main() {
	if len(os.Args) < 2 {
		runCmd(nil)
		return
	}

	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "install":
		installCmd(os.Args[2:])
	case "uninstall":
		uninstallCmd(os.Args[2:])
	case "add":
		addCmd(os.Args[2:])
	case "remove", "rm":
		removeCmd(os.Args[2:])
	case "list", "ls":
		listCmd(os.Args[2:])
	case "check":
		checkCmd(os.Args[2:])
	case "up":
		setEnabledCmd(true)
	case "down":
		setEnabledCmd(false)
	default:
		runCmd(os.Args[1:])
	}
}

func runFlags(fs *flag.FlagSet) (*time.Duration, *string, *string, *string) {
	interval := fs.Duration("interval", 30*time.Second, "how often to re-read the domain list and re-check routes")
	iface := fs.String("iface", "", "LAN interface to use (default: auto-detect from the default route)")
	gateway := fs.String("gateway", "", "LAN gateway IP to use (default: auto-detect from the default route)")
	netbirdBin := fs.String("netbird-bin", "", "path to the netbird CLI, used to check connection status (default: auto-detect)")
	return interval, iface, gateway, netbirdBin
}

func requireRoot(action string) {
	if os.Geteuid() != 0 {
		log.Fatalf("must run as root %s: try sudo", action)
	}
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	interval, iface, gateway, netbirdBin := runFlags(fs)
	fs.Parse(args)

	requireRoot("(routes modify the kernel routing table)")

	log.Printf("watching %s, interval=%s", config.Path, *interval)

	e := enforcer.New(*iface, *gateway, *netbirdBin)

	// Any abnormal exit from here on (panic) must still restore the routes
	// we've overridden, not just leave them pointed at the LAN.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic: %v, restoring routes before exiting", r)
			e.Close()
			panic(r)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	e.Reconcile()
	for {
		select {
		case <-ticker.C:
			e.Reconcile()
		case sig := <-sigCh:
			log.Printf("received %s, restoring routes and exiting", sig)
			e.Close()
			return
		}
	}
}

func installCmd(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	interval, iface, gateway, netbirdBin := runFlags(fs)
	label := fs.String("label", defaultLabel, "LaunchDaemon label")
	binPath := fs.String("bin-path", defaultBinPath, "path to install the binary to")
	fs.Parse(args)

	requireRoot("to install a LaunchDaemon")

	runArgs := []string{"run", "-interval", interval.String()}
	if *iface != "" {
		runArgs = append(runArgs, "-iface", *iface)
	}
	if *gateway != "" {
		runArgs = append(runArgs, "-gateway", *gateway)
	}
	if *netbirdBin != "" {
		runArgs = append(runArgs, "-netbird-bin", *netbirdBin)
	}

	if err := service.Install(*label, *binPath, runArgs); err != nil {
		log.Fatalf("install: %v", err)
	}
	fmt.Printf("installed and started %s (binary: %s, log: /var/log/%s.log)\n", *label, *binPath, *label)
	fmt.Printf("manage the domain list with: %s add|remove|list <domain>\n", os.Args[0])
}

func uninstallCmd(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	label := fs.String("label", defaultLabel, "LaunchDaemon label")
	fs.Parse(args)

	requireRoot("to uninstall a LaunchDaemon")
	if err := service.Uninstall(*label); err != nil {
		log.Fatalf("uninstall: %v", err)
	}
	fmt.Printf("uninstalled %s\n", *label)
}

func addCmd(args []string) {
	if len(args) == 0 {
		log.Fatal("usage: netbird-excluder add <domain> [domain...]")
	}
	requireRoot("to change the domain list")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	added := cfg.Add(args)
	if err := config.Save(cfg); err != nil {
		log.Fatalf("save config: %v", err)
	}

	if len(added) == 0 {
		fmt.Println("nothing to add (already present)")
		return
	}
	fmt.Printf("added: %s\n", strings.Join(added, ", "))
}

func removeCmd(args []string) {
	if len(args) == 0 {
		log.Fatal("usage: netbird-excluder remove <domain> [domain...]")
	}
	requireRoot("to change the domain list")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	removed := cfg.Remove(args)
	if err := config.Save(cfg); err != nil {
		log.Fatalf("save config: %v", err)
	}

	if len(removed) == 0 {
		fmt.Println("nothing to remove (not present)")
		return
	}
	fmt.Printf("removed: %s\n", strings.Join(removed, ", "))
}

func listCmd(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	state := "enabled"
	if !cfg.Enabled {
		state = "disabled (netbird-excluder down)"
	}
	nbState := "connected"
	if !netbird.Connected("") {
		nbState = "not connected"
	}
	fmt.Printf("state: %s, netbird: %s\n", state, nbState)

	if len(cfg.Domains) == 0 {
		fmt.Println("no domains configured")
		return
	}

	for _, domain := range cfg.Domains {
		printDomainRoute(domain)
	}
}

// checkCmd prints the same per-domain route line as "list", for domains not
// (necessarily) in the config - useful to preview what adding one would
// affect before running "add".
func checkCmd(args []string) {
	if len(args) == 0 {
		log.Fatal("usage: netbird-excluder check <domain> [domain...]")
	}
	for _, domain := range args {
		printDomainRoute(domain)
	}
}

func printDomainRoute(domain string) {
	ips, err := resolver.Lookup(domain)
	if err != nil {
		fmt.Printf("  %s: resolve error: %v\n", domain, err)
		return
	}
	for _, ip := range ips {
		gw, iface, err := routing.RouteInfo(ip)
		switch {
		case err != nil:
			fmt.Printf("  %-30s %-16s route unknown: %v\n", domain, ip, err)
		case gw == "":
			fmt.Printf("  %-30s %-16s via %s\n", domain, ip, iface)
		default:
			fmt.Printf("  %-30s %-16s via %s (%s)\n", domain, ip, iface, gw)
		}
	}
}

func setEnabledCmd(enabled bool) {
	requireRoot("to change enabled state")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	cfg.Enabled = enabled
	if err := config.Save(cfg); err != nil {
		log.Fatalf("save config: %v", err)
	}

	if enabled {
		fmt.Println("enabled - a running daemon will resume enforcing on its next check")
	} else {
		fmt.Println("disabled - a running daemon will restore its overrides and pause on its next check")
	}
}
