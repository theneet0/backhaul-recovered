# Backhaul v2.0.0-hotfix8 behavioral reconstruction

This repository contains a buildable, clean-room reconstruction made from the
retained `backhaul_premium` executable and its installer. It is **not** the
byte-for-byte proprietary source: the original Go program was UPX-packed and
built with `garble -tiny -literals`, so exact names, comments, layout, and some
implementation details cannot be recovered.

The recovered TCP path implements the wire behavior measured from the retained
`v2.0.0-hotfix8` binary. The other accepted transport names come from a public
clean-room compatibility baseline and are not claimed to be wire-equivalent to
the premium executable. See [RECOVERY_REPORT.md](RECOVERY_REPORT.md) for the
evidence and limitations, and [PRIVACY.md](PRIVACY.md) for the enforceable
network policy. The complete source-to-source protocol results are in
[TRANSPORT_MATRIX.md](TRANSPORT_MATRIX.md).

## Quick install from GitHub

The command below detects Linux AMD64 or ARM64, downloads the matching binary
and manager from the latest GitHub Release, verifies both files against
`SHA256SUMS`, installs them, and opens the interactive manager:

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/theneet0/backhaul-recovered/main/install.sh)
```

Run the manager again later with:

```sh
backhaul
```

Install without opening the interactive menu:

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/theneet0/backhaul-recovered/main/install.sh) --no-menu
```

Install a specific release tag:

```sh
BACKHAUL_VERSION=v2.0.0-hotfix8-recovered.3 \
  bash <(curl -fsSL https://raw.githubusercontent.com/theneet0/backhaul-recovered/main/install.sh)
```

The bootstrap contacts only GitHub raw content and GitHub Releases to obtain the
installer assets. It does not submit host information, configuration, tokens,
logs, traffic statistics, or telemetry. After installation, the manager and
runtime do not perform update checks in the background.

## Build

Go 1.25 or newer is required.

```sh
CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" \
  -o dist/backhaul_linux_amd64 ./cmd/backhaul
```

Build all GitHub Release assets locally:

```sh
./scripts/build-release.sh dist
```

Check the reconstructed version marker:

```sh
./dist/backhaul_linux_amd64 -v
```

## Automated GitHub releases

- `.github/workflows/ci.yml` runs tests, vet, and shell syntax checks on pushes
  and pull requests.
- `.github/workflows/release.yml` builds static Linux AMD64 and ARM64 binaries,
  creates `SHA256SUMS`, and publishes the assets whenever a `v*` tag is pushed.

Publish the first release after pushing the repository:

```sh
git tag v2.0.0-hotfix8-recovered.3
git push origin v2.0.0-hotfix8-recovered.3
```

A GitHub CLI helper is also included. From an authenticated local checkout it
creates the public repository when missing, pushes `main`, and pushes the
release tag:

```sh
./scripts/publish-github.sh theneet0/backhaul-recovered
```

The release contains:

- `backhaul_linux_amd64`
- `backhaul_linux_arm64`
- `backhaul.sh`
- `install.sh`
- `SHA256SUMS`

## Run a TCP tunnel

1. Copy `examples/server.toml` to the public server.
2. Copy `examples/client.toml` to the machine hosting the local service.
3. Use the same strong token in both files and replace `SERVER_IP` in the client
   file.
4. Start the server and then the client:

```sh
./backhaul_linux_amd64 -c server.toml
./backhaul_linux_amd64 -c client.toml
```

The example exposes TCP port `8080` on the server and relays it to
`127.0.0.1:8080` on the client.

## Verified scope

- Original binary server to reconstructed client: TCP data relay passed,
  including a second transfer after six seconds of heartbeat traffic.
- Reconstructed server and client: all 40 accepted transport names passed both
  TCP and UDP end-to-end relay tests.
- The Bash manager generated valid server and client configurations for every
  transport (80/80 parsed and validated).
- Original TCP authentication, worker request, RTT, heartbeat, destination
  framing, and bare-port target behavior are implemented and tested.
- `go test ./...` and `go vet ./...` pass with Go 1.25.5.

## Offline management script

The interactive manager remains usable without any network download when a
local binary is supplied:

```sh
BACKHAUL_BINARY="$PWD/dist/backhaul_linux_amd64" \
  ./installer/backhaul.sh --verify-local

sudo BACKHAUL_BINARY="$PWD/dist/backhaul_linux_amd64" \
  ./installer/backhaul.sh --install-local

sudo BACKHAUL_BINARY="$PWD/dist/backhaul_linux_amd64" \
  ./installer/backhaul.sh
```

The interactive mode generates TOML files and manages local systemd services,
status, logs, restart, and removal. The manager itself performs no core
download, self-update, external IP/ISP lookup, online dependency installation,
or telemetry request. Use `--list-transports` to print the 40 supported names.

## Privacy

- No license or entitlement server is contacted.
- No telemetry, analytics, host fingerprint, crash report, or usage report is
  generated or transmitted.
- Runtime network traffic is limited to listeners, peers, and targets supplied
  through configuration and the authenticated tunnel protocol.
- The optional Quick Install bootstrap downloads only explicitly named GitHub
  Release assets and verifies their SHA256 checksums.
- No scheduled updater or background reporting service is installed.

As with any tunnel, application traffic passes through the server selected by
the user. See [PRIVACY.md](PRIVACY.md) for the exact boundary and operational
hardening guidance.

## Important limitations

- TCP is the only transport validated against the retained executable.
- `kcp`, WebSocket, HTTP, gRPC, DNS, mux, `anytls`, raw, TUN/IPX, and related
  aliases are compatibility implementations, not proven premium equivalents.
- PSK/encryption and several tuning fields are parsed for configuration
  compatibility but are not complete reproductions of the premium feature set.
- The reconstruction has not received a security audit. Use it first in an
  isolated environment.
