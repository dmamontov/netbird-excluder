package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"netbird-excluder/internal/config"
	"netbird-excluder/internal/enforcer"
	"netbird-excluder/internal/netbird"
	"netbird-excluder/internal/resolver"
	"netbird-excluder/internal/routing"
	"netbird-excluder/internal/service"
)

const defaultLabel = "local.netbird-excluder"
const defaultBinPath = "/usr/local/bin/netbird-excluder"

// netbirdBin is a persistent flag shared by every subcommand that talks to
// the netbird CLI (run, install, list, check).
var netbirdBin string

func main() {
	root := &cobra.Command{
		Use:   "netbird-excluder",
		Short: "Force specific domains and IPs to route via the LAN gateway instead of through NetBird",
		// Errors here are about the input (a bad entry, a broken config
		// file), not about how the command was invoked.
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&netbirdBin, "netbird-bin", "", "path to the netbird CLI (default: auto-detect)")

	root.AddCommand(
		newRunCmd(),
		newInstallCmd(),
		newUninstallCmd(),
		newAddCmd(),
		newRemoveCmd(),
		newListCmd(),
		newCheckCmd(),
		newValidateCmd(),
		newUpCmd(),
		newDownCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func requireRoot(action string) {
	if os.Geteuid() != 0 {
		log.Fatalf("must run as root %s: try sudo", action)
	}
}

func newRunCmd() *cobra.Command {
	var interval time.Duration
	var iface, gateway string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run in the foreground, enforcing the configured exclusion list",
		RunE: func(cmd *cobra.Command, args []string) error {
			requireRoot("(routes modify the kernel routing table)")

			log.Printf("watching %s, interval=%s", config.Path, interval)

			e := enforcer.New(iface, gateway, netbirdBin)

			// Any abnormal exit from here on (panic) must still restore the
			// routes we've overridden, not just leave them pointed at the LAN.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("panic: %v, restoring routes before exiting", r)
					e.Close()
					panic(r)
				}
			}()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			e.Reconcile()
			for {
				select {
				case <-ticker.C:
					e.Reconcile()
				case sig := <-sigCh:
					log.Printf("received %s, restoring routes and exiting", sig)
					e.Close()
					return nil
				}
			}
		},
	}

	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second, "how often to re-read the exclusion list and re-check routes")
	cmd.Flags().StringVar(&iface, "iface", "", "LAN interface to use (default: auto-detect from the default route)")
	cmd.Flags().StringVar(&gateway, "gateway", "", "LAN gateway IP to use (default: auto-detect from the default route)")
	return cmd
}

func newInstallCmd() *cobra.Command {
	var interval time.Duration
	var iface, gateway, label, binPath string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and start a persistent LaunchDaemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			requireRoot("to install a LaunchDaemon")

			runArgs := []string{"run", "--interval", interval.String()}
			if iface != "" {
				runArgs = append(runArgs, "--iface", iface)
			}
			if gateway != "" {
				runArgs = append(runArgs, "--gateway", gateway)
			}
			if netbirdBin != "" {
				runArgs = append(runArgs, "--netbird-bin", netbirdBin)
			}

			if err := service.Install(label, binPath, runArgs); err != nil {
				return fmt.Errorf("install: %w", err)
			}
			fmt.Printf("installed and started %s (binary: %s, log: /var/log/%s.log)\n", label, binPath, label)
			fmt.Printf("manage the exclusion list by editing %s or with: netbird-excluder add|remove|list <domain|ip>\n", config.Path)
			return nil
		},
	}

	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second, "how often to re-read the exclusion list and re-check routes")
	cmd.Flags().StringVar(&iface, "iface", "", "LAN interface to use (default: auto-detect from the default route)")
	cmd.Flags().StringVar(&gateway, "gateway", "", "LAN gateway IP to use (default: auto-detect from the default route)")
	cmd.Flags().StringVar(&label, "label", defaultLabel, "LaunchDaemon label")
	cmd.Flags().StringVar(&binPath, "bin-path", defaultBinPath, "path to install the binary to")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	var label string

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the LaunchDaemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			requireRoot("to uninstall a LaunchDaemon")
			if err := service.Uninstall(label); err != nil {
				return fmt.Errorf("uninstall: %w", err)
			}
			fmt.Printf("uninstalled %s\n", label)
			return nil
		},
	}

	cmd.Flags().StringVar(&label, "label", defaultLabel, "LaunchDaemon label")
	return cmd
}

func newAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <domain|ip> [domain|ip...]",
		Short: "Add domain(s) and/or IPv4 address(es) to the exclusion list",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			requireRoot("to change the exclusion list")

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			added, err := cfg.Add(args)
			if err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			if len(added) == 0 {
				fmt.Println("nothing to add (already present)")
				return nil
			}
			fmt.Printf("added: %s\n", strings.Join(added, ", "))
			return nil
		},
	}
}

func newRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <domain|ip> [domain|ip...]",
		Aliases: []string{"rm"},
		Short:   "Remove domain(s) and/or IPv4 address(es) from the exclusion list",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			requireRoot("to change the exclusion list")

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			removed, err := cfg.Remove(args)
			if err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			if len(removed) == 0 {
				fmt.Println("nothing to remove (not present)")
				return nil
			}
			fmt.Printf("removed: %s\n", strings.Join(removed, ", "))
			return nil
		},
	}
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show the exclusion list, enabled state, and each entry's current route",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			state := "enabled"
			if !cfg.Enabled {
				state = "disabled (netbird-excluder down)"
			}
			nbState := "connected"
			if !netbird.Connected(netbirdBin) {
				nbState = "not connected"
			}
			fmt.Printf("state: %s, netbird: %s\n", state, nbState)

			if len(cfg.Domains) == 0 && len(cfg.IPs) == 0 {
				fmt.Printf("nothing excluded (add entries to %s or with: netbird-excluder add)\n", config.Path)
				return nil
			}

			nbIface, nbErr := netbird.InterfaceName(netbirdBin)
			if len(cfg.Domains) > 0 {
				fmt.Println("domains:")
				for _, domain := range cfg.Domains {
					printDomainRoute(domain, nbIface, nbErr == nil)
				}
			}
			if len(cfg.IPs) > 0 {
				fmt.Println("ips:")
				for _, ip := range cfg.IPs {
					printRoute(ip, ip, nbIface, nbErr == nil)
				}
			}
			return nil
		},
	}
}

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <domain|ip> [domain|ip...]",
		Short: "Preview a domain's or IP's current route without adding it",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nbIface, nbErr := netbird.InterfaceName(netbirdBin)
			for _, entry := range args {
				kind, norm, err := config.Classify(entry)
				switch {
				case err != nil:
					fmt.Printf("  %s: %v\n", entry, err)
				case kind == config.KindIP:
					printRoute(norm, norm, nbIface, nbErr == nil)
				default:
					printDomainRoute(norm, nbIface, nbErr == nil)
				}
			}
			return nil
		},
	}
}

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [file]",
		Short: "Check a config file for errors without applying it (default: " + config.Path + ")",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.Path
			if len(args) == 1 {
				path = args[0]
			}
			cfg, err := config.LoadFile(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			state := "enabled"
			if !cfg.Enabled {
				state = "disabled"
			}
			fmt.Printf("%s: OK (%s, %d domain(s), %d IP(s))\n", path, state, len(cfg.Domains), len(cfg.IPs))
			return nil
		},
	}
}

// printDomainRoute prints one line per resolved IP for domain (see
// printRoute).
func printDomainRoute(domain, nbIface string, nbOK bool) {
	ips, err := resolver.Lookup(domain)
	if err != nil {
		fmt.Printf("  %s: resolve error: %v\n", domain, err)
		return
	}
	for _, ip := range ips {
		printRoute(domain, ip, nbIface, nbOK)
	}
}

// printRoute prints one line for ip (labelled with the entry it came from),
// saying whether it's currently going through NetBird's interface (nbIface,
// valid only if nbOK) or directly.
func printRoute(label, ip, nbIface string, nbOK bool) {
	gw, iface, err := routing.RouteInfo(ip)
	switch {
	case err != nil:
		fmt.Printf("  %-30s %-16s route unknown: %v\n", label, ip, err)
	case nbOK && iface == nbIface:
		fmt.Printf("  %-30s %-16s via NetBird (%s)\n", label, ip, iface)
	case gw == "":
		fmt.Printf("  %-30s %-16s direct via %s\n", label, ip, iface)
	default:
		fmt.Printf("  %-30s %-16s direct via %s (%s)\n", label, ip, iface, gw)
	}
}

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Resume enforcement (undo 'down')",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return setEnabled(true)
		},
	}
}

func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Restore all overrides and pause enforcement",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return setEnabled(false)
		},
	}
}

func setEnabled(enabled bool) error {
	requireRoot("to change enabled state")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.Enabled = enabled
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if enabled {
		fmt.Println("enabled - a running daemon will resume enforcing on its next check")
	} else {
		fmt.Println("disabled - a running daemon will restore its overrides and pause on its next check")
	}
	return nil
}
