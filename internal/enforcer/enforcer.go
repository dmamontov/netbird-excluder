package enforcer

import (
	"log"

	"netbird-excluder/internal/config"
	"netbird-excluder/internal/netbird"
	"netbird-excluder/internal/resolver"
	"netbird-excluder/internal/routing"
)

// ownedRoute tracks, for one IP, which sources (a listed domain resolving to
// it, or the IP itself being listed) currently need it forced via the LAN
// gateway and what its route looked like before we touched it, so it can be
// put back exactly as NetBird had it.
type ownedRoute struct {
	sources     map[string]bool
	origGateway string
	origIface   string
	overridden  bool
}

// Enforcer routes listed domains and IPs via the LAN gateway instead of
// whatever route NetBird has pushed for them. The exclusion list and
// enabled/disabled state are re-read from config on every Reconcile, so
// edits to the file or "add"/"remove"/"up"/"down" run from another
// invocation take effect without a restart.
// PinnedIface/PinnedGateway override auto-detection; leave empty to
// re-detect the LAN gateway from the default route on every Reconcile
// (handles switching Wi-Fi networks).
type Enforcer struct {
	PinnedIface   string
	PinnedGateway string
	NetbirdBin    string

	LANIface   string
	LANGateway string

	owners         map[string]*ownedRoute
	netbirdChecked bool
	netbirdUp      bool
	enabledChecked bool
	enabled        bool
	configErr      string
}

func New(pinnedIface, pinnedGateway, netbirdBin string) *Enforcer {
	return &Enforcer{
		PinnedIface:   pinnedIface,
		PinnedGateway: pinnedGateway,
		NetbirdBin:    netbirdBin,
		owners:        make(map[string]*ownedRoute),
	}
}

// checkNetbird reports whether NetBird is currently connected, logging only
// on transitions so the log isn't spammed once a tick.
func (e *Enforcer) checkNetbird() bool {
	up := netbird.Connected(e.NetbirdBin)
	switch {
	case !e.netbirdChecked && !up:
		log.Printf("NetBird is not connected; will start enforcing once it is")
	case e.netbirdChecked && up && !e.netbirdUp:
		log.Printf("NetBird is back up; resuming route enforcement")
	case e.netbirdChecked && !up && e.netbirdUp:
		log.Printf("NetBird went down; restoring overrides and pausing until it reconnects")
	}
	e.netbirdChecked = true
	e.netbirdUp = up
	return up
}

// checkEnabled reports whether the config's enabled flag is set (toggled via
// the "up"/"down" commands), logging only on transitions.
func (e *Enforcer) checkEnabled(cfgEnabled bool) bool {
	switch {
	case !e.enabledChecked && !cfgEnabled:
		log.Printf("disabled (netbird-excluder down); will start enforcing once enabled")
	case e.enabledChecked && cfgEnabled && !e.enabled:
		log.Printf("enabled (netbird-excluder up); resuming route enforcement")
	case e.enabledChecked && !cfgEnabled && e.enabled:
		log.Printf("disabled (netbird-excluder down); restoring overrides and pausing")
	}
	e.enabledChecked = true
	e.enabled = cfgEnabled
	return cfgEnabled
}

// loadConfig reads the config, logging a broken file once (not every tick)
// and again once it's fixed. On error the caller keeps the current routes
// rather than tearing them down over a typo mid-edit.
func (e *Enforcer) loadConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		if msg := err.Error(); msg != e.configErr {
			log.Printf("load config: %v (keeping current routes until it's fixed)", err)
			e.configErr = msg
		}
		return nil
	}
	if e.configErr != "" {
		log.Printf("config is valid again; resuming")
		e.configErr = ""
	}
	return cfg
}

func (e *Enforcer) Reconcile() {
	cfg := e.loadConfig()
	if cfg == nil {
		return
	}

	if !e.checkEnabled(cfg.Enabled) {
		e.Close()
		return
	}

	if !e.checkNetbird() {
		e.Close()
		return
	}

	if err := e.refreshLAN(); err != nil {
		log.Printf("detect LAN gateway: %v (skipping this pass)", err)
		return
	}

	seenBySource := make(map[string]map[string]bool, len(cfg.Domains)+len(cfg.IPs))

	for _, domain := range cfg.Domains {
		ips, err := resolver.Lookup(domain)
		if err != nil {
			log.Printf("resolve %s: %v", domain, err)
			continue
		}

		src := "domain " + domain
		seen := make(map[string]bool, len(ips))
		seenBySource[src] = seen

		for _, ip := range ips {
			seen[ip] = true
			e.ensureOwned(src, ip)
		}
	}

	for _, ip := range cfg.IPs {
		src := "listed IP " + ip
		seenBySource[src] = map[string]bool{ip: true}
		e.ensureOwned(src, ip)
	}

	e.releaseStale(seenBySource)
}

func (e *Enforcer) refreshLAN() error {
	if e.PinnedIface != "" && e.PinnedGateway != "" {
		e.LANIface, e.LANGateway = e.PinnedIface, e.PinnedGateway
		return nil
	}

	gw, iface, err := routing.DefaultGateway()
	if err != nil {
		return err
	}
	if e.PinnedIface != "" {
		iface = e.PinnedIface
	}
	if e.PinnedGateway != "" {
		gw = e.PinnedGateway
	}

	if e.LANIface != "" && (iface != e.LANIface || gw != e.LANGateway) {
		log.Printf("LAN changed: %s via %s (was %s via %s)", gw, iface, e.LANGateway, e.LANIface)
	}
	e.LANIface, e.LANGateway = iface, gw
	return nil
}

// ensureOwned records src as an owner of ip and makes sure ip currently
// routes via the LAN gateway, re-overriding it if NetBird (or a network
// change) has since pointed it elsewhere. The route observed the first time
// an IP is seen is kept as the "original" to restore later.
func (e *Enforcer) ensureOwned(src, ip string) {
	curGw, curIface, err := routing.RouteInfo(ip)
	if err != nil {
		log.Printf("check route for %s (%s): %v", ip, src, err)
	}

	entry, exists := e.owners[ip]
	if !exists {
		entry = &ownedRoute{sources: make(map[string]bool), origGateway: curGw, origIface: curIface}
		e.owners[ip] = entry
	}
	entry.sources[src] = true

	if curIface == e.LANIface && curGw == e.LANGateway {
		return
	}

	if err := routing.DeleteHostRoute(ip); err != nil {
		log.Printf("delete existing route for %s (%s): %v", ip, src, err)
	}
	if err := routing.AddHostRoute(ip, e.LANGateway); err != nil {
		log.Printf("force %s (%s) via LAN: %v", ip, src, err)
		return
	}
	entry.overridden = true
	log.Printf("forced %s (%s) via LAN gateway %s", ip, src, e.LANGateway)
}

func (e *Enforcer) releaseStale(seenBySource map[string]map[string]bool) {
	for ip, entry := range e.owners {
		for src := range entry.sources {
			if !seenBySource[src][ip] {
				delete(entry.sources, src)
			}
		}
		if len(entry.sources) == 0 {
			e.restore(ip, entry)
			delete(e.owners, ip)
		}
	}
}

// restore puts ip's route back the way it was before this Enforcer touched
// it. If it was never actually overridden (already matched the LAN gateway),
// nothing is changed.
func (e *Enforcer) restore(ip string, entry *ownedRoute) {
	if !entry.overridden {
		return
	}

	if err := routing.DeleteHostRoute(ip); err != nil {
		log.Printf("remove override for %s: %v", ip, err)
	}

	if entry.origIface == "" {
		log.Printf("released %s (no prior route known, left on default route)", ip)
		return
	}

	var err error
	if entry.origGateway != "" {
		err = routing.AddHostRoute(ip, entry.origGateway)
	} else {
		err = routing.AddInterfaceRoute(ip, entry.origIface)
	}
	if err != nil {
		log.Printf("restore original route for %s via %s: %v", ip, entry.origIface, err)
		return
	}
	log.Printf("restored %s via %s (as it was before)", ip, entry.origIface)
}

// Close restores every route this Enforcer has overridden. Safe to call
// multiple times and from a recovered panic.
func (e *Enforcer) Close() {
	for ip, entry := range e.owners {
		e.restore(ip, entry)
	}
	e.owners = make(map[string]*ownedRoute)
}
