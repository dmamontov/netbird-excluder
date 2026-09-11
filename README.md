# netbird-excluder

**macOS only.** Forces specific domains to route through the LAN gateway
instead of through [NetBird](https://netbird.io). It shells out to `/sbin/route`
and `launchctl`, both macOS-specific - it will not build or run correctly on
Linux or Windows.

## Why

NetBird's domain-based network routes resolve a domain (e.g. an internal
resource proxied through Cloudflare) to its current IP and install a `/32`
host route for it into the tunnel. Cloudflare (and similar CDNs/anycast
providers) hand out the same small set of edge IPs to thousands of unrelated
domains. If your NetBird admin has routed one such IP, *any* other site that
happens to share it gets silently pulled into the tunnel too and breaks -
often you can't tell why, since your NetBird routes look correct.

You can't fix this in NetBird's own config (no access, or it would break the
resource it was set up for). `netbird-excluder` fixes it locally: it keeps a
domain blacklist, and for every IP those domains currently resolve to, it
holds an explicit host route via your LAN gateway that overrides whatever
NetBird has pushed - re-asserting it every few seconds so NetBird's own
reconciliation (or a Wi-Fi network change) can't quietly undo it.

## How it works

- On each pass it resolves every domain in the blacklist and, for each IP,
  checks the route the kernel currently uses (`route -n get -host <ip>`).
- If that route isn't already via the LAN gateway, it deletes it and adds a
  static host route via the LAN gateway/interface, overriding NetBird.
- The very first route seen for an IP is remembered as the "original" route.
  When a domain stops resolving to that IP, or the tool stops/panics/is
  disabled, the original route is restored - not just deleted.
- The LAN gateway/interface is re-detected from the default route on every
  pass (unless pinned with `-iface`/`-gateway`), so switching Wi-Fi networks
  is handled automatically.
- NetBird's own connection status (`netbird status -C ready`) is checked
  every pass. While NetBird is disconnected there's nothing to override, so
  all overrides are restored and enforcement pauses until it reconnects.

No IP-collision protection is attempted for domains you *don't* blacklist:
Cloudflare/anycast IP sharing turned out to be so common in practice (one
shared IP can be claimed by dozens of legitimate NetBird domains at once)
that trying to detect and avoid that automatically made the tool unable to
fix the exact problem it exists for. Only blacklist a domain if you're fine
with its traffic - and anything else that happens to share its IP at that
moment - leaving the tunnel.

## Requirements

- macOS (uses `/sbin/route` and `launchctl`; no other platform is supported).
- Root privileges for anything that changes routes or the domain list
  (`run`, `install`, `uninstall`, `add`, `remove`, `up`, `down`). `list` does
  not need root.
- The `netbird` CLI reachable at `/usr/local/bin/netbird`,
  `/opt/homebrew/bin/netbird`, or on `PATH` (override with `-netbird-bin`).

## Install

Download and install the latest release in one command:

```bash
curl -fsSL https://raw.githubusercontent.com/dmamontov/netbird-excluder/main/install.sh | bash
```

This downloads the binary for your Mac's architecture, installs it to
`/usr/local/bin/netbird-excluder`, and sets it up as a persistent
LaunchDaemon (running with an empty domain list until you `add` some - see
[Usage](#usage) below). Re-run the same command any time to update to the
latest version; it leaves your domain list and the running service as they
are.

## Build from source

```bash
go build -o netbird-excluder .
```

## Usage

### Manage the domain list

The blacklist and enabled/disabled state live in
`/etc/netbird-excluder/config.json`, shared by every command below and by a
running daemon - changes take effect on its next reconcile pass, no restart
needed.

```bash
sudo ./netbird-excluder add example.com another-site.com
sudo ./netbird-excluder remove example.com     # alias: rm
./netbird-excluder list                        # alias: ls, no root needed
```

`list` shows whether enforcement is enabled, whether NetBird is connected,
and for each domain its current resolved IP(s) and which interface/gateway
they're actually routed through right now.

### Pause / resume enforcement

```bash
sudo ./netbird-excluder down   # restore all overrides, pause enforcement
sudo ./netbird-excluder up     # resume
```

This only affects `netbird-excluder`'s own routing overrides; it does not
touch NetBird itself (see `netbird down`/`netbird up` for that).

### Run in the foreground

```bash
sudo ./netbird-excluder run -interval 30s
```

Flags (all optional): `-interval` (default `30s`), `-iface`/`-gateway` (pin
the LAN gateway instead of auto-detecting it from the default route),
`-netbird-bin` (path to the `netbird` CLI).

Stop with Ctrl+C - it restores every route it overrode before exiting. The
same restore runs if the process panics; it cannot run on `kill -9` or a
power loss, which is a limitation of any user-space daemon, not specific to
this tool.

### Install as a persistent service

```bash
sudo ./netbird-excluder install -interval 30s
```

Installs the binary to `/usr/local/bin/netbird-excluder`, writes a
LaunchDaemon plist to `/Library/LaunchDaemons/local.netbird-excluder.plist`
(`RunAtLoad` + `KeepAlive`), and loads it. Logs go to
`/var/log/local.netbird-excluder.log`. Accepts the same `-interval` /
`-iface` / `-gateway` / `-netbird-bin` flags as `run`, plus `-label` and
`-bin-path` to change the defaults. Manage the domain list with
`add`/`remove`/`list` as usual - the installed daemon reads the same config
file.

```bash
sudo ./netbird-excluder uninstall
```

Unloads and removes the LaunchDaemon plist. It does not touch
`/etc/netbird-excluder/config.json` or currently-active route overrides.
