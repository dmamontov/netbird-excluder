# netbird-excluder

**macOS only.** Forces specific domains, IPv4 addresses and IPv4 ranges to route through
the LAN gateway instead of through [NetBird](https://netbird.io). It shells out to `/sbin/route`
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
resource it was set up for). `netbird-excluder` fixes it locally: it keeps an
exclusion list of domains, IPv4 addresses and CIDR ranges, and for every
listed IP or range (and every IP the listed domains currently resolve to) it
holds an explicit route via your LAN gateway that overrides whatever
NetBird has pushed - re-asserting it every few seconds so NetBird's own
reconciliation (or a Wi-Fi network change) can't quietly undo it.

## How it works

- On each pass it resolves every listed domain and, for each resulting IP
  and each listed IP or range, checks the route the kernel currently uses
  (`route -n get -host <ip>`, or `-net <cidr>` for a range).
- If that route isn't already via the LAN gateway, it deletes it and adds a
  static host route (network route for a range) via the LAN
  gateway/interface, overriding NetBird.
- The very first route seen for an IP or range is remembered as the "original" route.
  When no listed entry needs that IP any more (a domain stops resolving to
  it, or it's removed from the list), or the tool stops/panics/is disabled,
  the original route is restored - not just deleted.
- The LAN gateway/interface is re-detected from the default route on every
  pass (unless pinned with `--iface`/`--gateway`), so switching Wi-Fi
  networks is handled automatically.
- NetBird's own connection status (`netbird status -C ready`) is checked
  every pass. While NetBird is disconnected there's nothing to override, so
  all overrides are restored and enforcement pauses until it reconnects.

No IP-collision protection is attempted for domains you *don't* exclude:
Cloudflare/anycast IP sharing turned out to be so common in practice (one
shared IP can be claimed by dozens of legitimate NetBird domains at once)
that trying to detect and avoid that automatically made the tool unable to
fix the exact problem it exists for. Only exclude a domain or IP if you're
fine with its traffic - and anything else that happens to share its IP at that
moment - leaving the tunnel.

## Requirements

- macOS (uses `/sbin/route` and `launchctl`; no other platform is supported).
- Root privileges for anything that changes routes or the exclusion list
  (`run`, `install`, `uninstall`, `add`, `remove`, `up`, `down`). `list`,
  `check` and `validate` do not need root.
- The `netbird` CLI reachable at `/usr/local/bin/netbird`,
  `/opt/homebrew/bin/netbird`, or on `PATH` (override with `--netbird-bin`).

## Install

Download and install the latest release in one command:

```bash
curl -fsSL https://raw.githubusercontent.com/dmamontov/netbird-excluder/main/install.sh | bash
```

This downloads the binary for your Mac's architecture, installs it to
`/usr/local/bin/netbird-excluder`, and sets it up as a persistent
LaunchDaemon (running with an empty exclusion list until you add entries -
see [Usage](#usage) below). Re-run the same command any time to update to
the latest version; it leaves your exclusion list and the running service as
they are.

## Build from source

```bash
go build -o netbird-excluder .
```

## Usage

### Manage the exclusion list

The exclusion list and enabled/disabled state live in
`/etc/netbird-excluder/config.yaml`, shared by every command below and by a
running daemon - changes take effect on its next reconcile pass, no restart
needed. You can edit the file by hand:

```yaml
# false pauses enforcement and restores every overridden route.
enabled: true
exclude:
  # Re-resolved on every pass; every IPv4 address they currently resolve to
  # is routed via the LAN gateway instead of through NetBird.
  domains:
    - example.com
    - another-site.com
  # IPv4 addresses or CIDR ranges (e.g. 10.0.0.0/8) to route via the LAN
  # gateway instead of NetBird.
  ips:
    - 104.16.12.34
    - 160.79.104.0/23
```

- `ips` takes single IPv4 addresses and IPv4 CIDR ranges; IPv6 is rejected.
  A range must have its host bits zeroed (`10.1.2.3/8` is an error that
  suggests `10.0.0.0/8`), `/0` is rejected, and `/32` is stored as the
  plain address.
- A range is overridden with a single network route, which beats NetBird
  routes that are the same size or wider - but not NetBird's more specific
  routes inside it, such as the `/32` host routes its domain-based routes
  install. If an address inside the range still goes through NetBird, list
  that domain or address too.
- Domains are lowercased and a trailing dot is dropped; duplicates are
  ignored. An IP under `domains` is an error, so the two lists can't be
  mixed up silently.
- Unknown keys are an error, so a typo like `domain:` can't quietly turn
  exclusions off.
- If the file is broken (bad YAML, invalid entry), a running daemon logs the
  error once and keeps the current routes as they are until it's fixed.
  Check your edits first with:

```bash
./netbird-excluder validate                 # or: validate path/to/other.yaml
#   /etc/netbird-excluder/config.yaml: OK (enabled, 2 domain(s), 2 IP(s)/range(s))
```

Or manage it from the CLI - entries are detected as domain or IP
automatically, and your comments and ordering in the file are preserved:

```bash
sudo ./netbird-excluder add example.com 104.16.12.34 160.79.104.0/23
sudo ./netbird-excluder remove example.com     # alias: rm
./netbird-excluder list                        # alias: ls, no root needed
```

Upgrading from a version that used `/etc/netbird-excluder/config.json`: the
first command run as root (including the daemon itself) converts it to
`config.yaml` and keeps the old file as `config.json.bak`. IPs that were
added as domains are moved to `ips`; anything invalid is dropped with a log
line. Until then, unprivileged commands keep reading the old file.

`list` shows whether enforcement is enabled, whether NetBird is connected,
and for each domain its current resolved IP(s) - and for each listed IP -
whether it's actually going through NetBird or direct right now (NetBird's interface is found by
asking it for its own overlay IP and matching it against the local
interfaces, not by guessing from an interface name).

To preview a domain or IP before adding it - no root needed, doesn't touch
the config or any route:

```bash
./netbird-excluder check some-site.com 104.16.12.34
#   some-site.com                  8.6.112.0        via NetBird (utun100)
#   104.16.12.34                   104.16.12.34     direct via en0 (192.168.1.1)
```

### Pause / resume enforcement

```bash
sudo ./netbird-excluder down   # restore all overrides, pause enforcement
sudo ./netbird-excluder up     # resume
```

This only affects `netbird-excluder`'s own routing overrides; it does not
touch NetBird itself (see `netbird down`/`netbird up` for that).

### Run in the foreground

```bash
sudo ./netbird-excluder run --interval 30s
```

Flags (all optional): `--interval` (default `30s`), `--iface`/`--gateway`
(pin the LAN gateway instead of auto-detecting it from the default route),
`--netbird-bin` (path to the `netbird` CLI).

Stop with Ctrl+C - it restores every route it overrode before exiting. The
same restore runs if the process panics; it cannot run on `kill -9` or a
power loss, which is a limitation of any user-space daemon, not specific to
this tool.

### Install as a persistent service

```bash
sudo ./netbird-excluder install --interval 30s
```

Installs the binary to `/usr/local/bin/netbird-excluder`, writes a
LaunchDaemon plist to `/Library/LaunchDaemons/local.netbird-excluder.plist`
(`RunAtLoad` + `KeepAlive`), and loads it. Logs go to
`/var/log/local.netbird-excluder.log`. Accepts the same `--interval` /
`--iface` / `--gateway` / `--netbird-bin` flags as `run`, plus `--label` and
`--bin-path` to change the defaults. Manage the exclusion list by editing
the config file or with `add`/`remove`/`list` as usual - the installed daemon
reads the same config file.

```bash
sudo ./netbird-excluder uninstall
```

Unloads and removes the LaunchDaemon plist. It does not touch
`/etc/netbird-excluder/config.yaml` or currently-active route overrides.
