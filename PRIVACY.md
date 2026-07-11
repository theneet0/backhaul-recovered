# Privacy and network policy

This reconstruction has no license server, entitlement check, telemetry,
analytics, crash reporting, public-IP or geolocation lookup, automatic
background update, or vendor-controlled runtime reporting endpoint.

## Network activity that remains

Backhaul is a network tunnel, so it cannot operate without network traffic. Its
runtime connections are limited to values supplied through the local
configuration or through the authenticated tunnel protocol:

- A server listens on `[listener].bind_addr` and the local addresses derived
  from `[ports].mapping`.
- A client connects to `[dialer].remote_addr`.
- A client worker connects to the target carried over the authenticated tunnel.
  That target is derived from the server operator's `[ports].mapping`.
- Optional transports open or connect only to their configured listener,
  remote, target, DNS, TLS, HTTP, WebSocket, gRPC, KCP, or UDP addresses.
- DNS resolution occurs only when a configured address contains a hostname.

There are no background connections when the program is only invoked with
`-v`. No runtime code downloads updates or reports version, token, address,
hostname, hardware, traffic, or usage information to the project author.

Tunnel traffic necessarily passes through the server selected by the user. A
server operator may be able to observe connection metadata or unencrypted
payloads; that is part of operating a tunnel and is not project telemetry. Use
an authenticated encrypted transport when payload confidentiality is required.

## GitHub Quick Install boundary

The optional root-level `install.sh` is intentionally network-enabled. During
an explicit install or update command, it downloads these files from the chosen
GitHub Release:

- the binary matching Linux AMD64 or ARM64;
- `backhaul.sh`;
- `SHA256SUMS`.

It verifies the binary and manager against the published SHA256 list before
installation. The default repository is `theneet0/backhaul-recovered`; it can
be changed explicitly with `BACKHAUL_GITHUB_REPO` or `--repo`.

The bootstrap does not query a public-IP, geolocation, ISP, hostname, hardware,
machine identifier, license, analytics, or telemetry service. It does not send
configuration files, tokens, logs, tunnel destinations, or traffic statistics.
It installs no timer, cron job, update daemon, or scheduled network task.

As with any direct HTTPS download, GitHub and the network path can observe the
source IP, requested repository, release asset, timestamp, and normal HTTP/TLS
metadata. Use the offline installation method when this download metadata is
not acceptable.

## Logs

Runtime logs go to standard output. They can contain configured addresses and
connection metadata. The program does not upload those logs. Their retention is
controlled by the shell, service manager, or logging system used to run it.

## Offline management script

`installer/backhaul.sh` accepts a local binary, validates its reconstructed
version marker, and installs it under the locally configured Backhaul directory.
In interactive mode it can write TOML files, create or remove systemd units,
read service status and journals, and inspect the local interface address to
suggest configuration defaults.

It does not:

- contact a package, update, license, or project server;
- download a binary or script;
- query a public IP, country, ISP, hostname, or machine identifier service;
- install online dependencies;
- create a scheduled update or background reporting task.

Use `--verify-local` to validate a binary without changing the filesystem. The
local interface/IP values used as prompt defaults are never transmitted by the
manager.

## Enforced checks

The privacy regression suite keeps the executable runtime and offline manager
free of hard-coded external URLs, license/telemetry code, public-IP lookups, and
online package-manager commands:

```sh
go test ./internal/privacy -count=1
```

The network-enabled root `install.sh` is outside that offline-manager invariant
and is limited to the documented GitHub Release download flow.

For stricter operating-system isolation, use IP literals instead of hostnames,
keep tunnel targets on loopback, and apply an egress firewall that permits only
the chosen peer address and port.

## Publishing note

The current files are sanitized, but earlier commits in the private recovery
branch contain the decoded legacy installer used during analysis. Publish a
fresh repository made from the privacy-clean source snapshot rather than
publishing the private recovery history.
