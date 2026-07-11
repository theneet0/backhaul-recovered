# Recovery report

## Result

A fully offline installer, buildable Go source tree, tests, an interoperability
harness, and a static linux/amd64 binary were reconstructed. Exact source
recovery was not possible because the executable was compiled with Go 1.25,
obfuscated with `garble` (`tiny` and `literals`), and then packed with UPX.

The all-transport validated result is versioned as
`v2.0.0-hotfix8-recovered.3` to prevent it from being confused with the
original proprietary build.

## Input identity

| Artifact | Size | SHA-256 |
|---|---:|---|
| Original archive `backhaul.tar.gz` | — | `dfbd5fd4df31b423facc8e00bc956754bf7b252963a58ff83ec7de5f5e11a93f` |
| Retained `backhaul_premium` | 5,676,472 bytes | `94719ee072a296cb4e43f91d0b1b060e667111b7ace72f0d0eb5933f2d5b3496` |
| Decoded legacy installer (analysis input; replaced) | 30,213 bytes | `d6d3017ffbfe0d2a4304873be15d096459e5c396e196c01695d90acf5d08b40b` |

The behavioral starting point was the public `monhacer/backhaul-freemium`
clean-room repository at commit
`3b49c29dbd8bb70998c8a116240f4caa71cc4e8c`, dated 2026-03-04. All claims of
original compatibility below are based on measurements against the retained
executable rather than provenance from that baseline.

## Confirmed TCP wire format

Typed payloads use this frame:

```text
uint16 big-endian payload length | uint8 signal | payload bytes
```

Control messages that have no payload are a single signal byte.

| Signal | Value | Confirmed behavior |
|---|---:|---|
| Closed | `0x00` | Close control channel |
| Heartbeat | `0x01` | Server sends; client echoes |
| Channel | `0x02` | Authentication response and worker request |
| RTT | `0x05` | Server sends; client echoes |
| Hello | `0x06` | Client authentication frame |
| TCP target | `0x10` | Destination for a worker connection |

For token `secret`, the measured client authentication frame is
`00 06 06 73 65 63 72 65 74`; the server response changes the signal byte to
`02`. A loopback TCP mapping is sent to the original client as a bare decimal
port, for example `00 05 10 33 39 30 38 32` for port `39082`.

Signal `0x11` is implemented as the adjacent UDP target value, but it was not
dynamically confirmed and is not included in the verified scope.

## Validation

| Check | Result |
|---|---|
| Decoded installer syntax (`bash -n`) | PASS |
| Focused framing and target tests | PASS |
| Reconstructed server/client TCP echo | PASS |
| Original server/reconstructed client TCP echo | PASS |
| Second original-server transfer after 6 seconds | PASS |
| Full Go suite (`go test ./... -count=1`) | PASS |
| Static checks (`go vet ./...`) | PASS |
| Version output | PASS |
| Privacy regression suite | PASS |
| All 40 transports, TCP relay | PASS (40/40) |
| All 40 transports, UDP relay | PASS (40/40) |
| Bash-generated server/client configs | PASS (80/80) |
| Bash manager syntax, verification, and local install | PASS |

The retained original client established control channels and consumed the same
worker frames, but did not complete a localhost relay in this analysis
environment. The same failure occurred when both endpoints were the retained
original executable. Consequently, reverse-direction live data compatibility
is not claimed; instead, the reconstructed server output was matched to a
captured original-server worker stream.

## Final build

The final build was produced with official Go 1.25.5:

```sh
CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" \
  -o dist/backhaul_recovered_linux_amd64 ./cmd/backhaul
```

| Property | Value |
|---|---|
| Target | linux/amd64 |
| Linkage | Static |
| ELF symbols | Stripped |
| Size | 13,631,672 bytes |
| SHA-256 | `7cec15aacd8f16c78f7b0f1d003ad55ea5508faa1e12e7ef54fdb3e0e1818764` |

## Limitations and remaining risk

- Original identifiers, comments, exact concurrency structure, and proprietary
  algorithms cannot be reconstructed from the garbled binary.
- Only the TCP path has direct interoperability evidence.
- Advanced transports passed recovered-server/recovered-client localhost tests,
  but are not proven wire-equivalent to the premium binary. Production network
  testing is still recommended.
- TUN/IPX names are mapped to user-space compatibility paths; the premium
  kernel-level behavior was not reproduced.
- Security configuration fields beyond the token/TLS paths do not imply full
  premium cryptographic equivalence.
- The legacy manager was sanitized and connected to local recovered builds. It
  keeps interactive TOML/systemd/status/log management but has no core download,
  self-update, external lookup, online dependency, telemetry, or scheduler
  behavior.

## Privacy hardening

The Go runtime audit found no license enforcement, telemetry, analytics, host
fingerprinting, or hard-coded project endpoint. The decoded legacy installer
did contain fixed download/update destinations and a public-IP country/ISP
lookup; those paths were removed from the publishable manager and replaced by
local-binary installation and verification.

`internal/privacy/privacy_test.go` prevents those executable behaviors from
returning. The complete public network policy is documented in
[PRIVACY.md](PRIVACY.md).

See [WORKLOG.md](WORKLOG.md) for the commit-by-commit continuation record.
