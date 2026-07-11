# Recovery worklog

## Current state

- Active branch: `validation/all-transports`.
- The previous sandbox and its local Git history are unavailable.
- The connected GitHub account currently exposes no repositories, so no remote branch or commit could be restored.
- The original archive was recovered as `../recovered-input/backhaul.tar.gz`.
- Archive SHA-256: `dfbd5fd4df31b423facc8e00bc956754bf7b252963a58ff83ec7de5f5e11a93f`.
- Known completed analysis (not repeated): Go 1.25 binary, UPX-packed, garble-obfuscated with `tiny` and `literals`, embedded version `v2.0.0-hotfix8`.
- Confirmed TCP wire findings: control authentication `0x06/0x02`, alternate connection request `0x02`, TCP destination signal `0x10`, heartbeat `0x01`, RTT `0x05`, and closed `0x00`.

## Completed in this resumed workspace

- Verified the current workspace had no usable Git repository, branch, commits, or changed files.
- Recovered and checksum-verified the original archive.
- Confirmed the archive contains `backhaul.sh` (63,557 bytes) and `backhaul_premium` (5,676,472 bytes).
- Restored the obfuscated three-line installer to a readable 942-line Bash script without executing its decoded payload.
- Validated the restored installer with `bash -n`.
- Located the same-day clean-room compatibility baseline `monhacer/backhaul-freemium` at commit `3b49c29dbd8bb70998c8a116240f4caa71cc4e8c` (2026-03-04) and imported its Go source as a behavioral starting point.
- Kept the reference checkout under ignored `reference/`; only source files needed for reconstruction were imported.
- Retargeted the baseline to Go 1.25 and marked the binary identity as `v2.0.0-hotfix8-recovered.1` so it cannot be mistaken for the byte-identical proprietary build.
- Replaced the baseline TCP handshake with the captured `v2.0.0-hotfix8` binary framing.
- Implemented the original control-channel authentication, worker requests, RTT exchange, bidirectional heartbeat, TCP destination frames, and loopback-port normalization.
- Added focused framing/target tests, an internal TCP relay test, and a localhost original-binary interoperability harness.
- Confirmed the recovered client exchanges data with the retained original server before and after a six-second heartbeat stability interval.
- Captured the original server-to-client worker stream and matched the recovered server's wire output, including the original bare-port target format.
- Ran the complete Go test and vet suites successfully.
- Produced the final reproducible static, stripped linux/amd64 build with Go 1.25.5, `CGO_ENABLED=0`, and `-buildvcs=false`.
- Added build/run instructions, working TCP example configurations, and a recovery report that separates confirmed behavior from compatibility-only paths.
- Prepared the final delivery layout to include the committed source, static binary, checksums, build metadata, and a Git bundle containing the recovery history.
- Audited the Go runtime for license checks, telemetry, analytics, host fingerprinting, and hard-coded outbound endpoints; none are present.
- Removed downloads, public-IP lookup, self-update, online package installation, and vendor branding from the recovered Bash workflow.
- Added privacy regression tests that reject runtime license/telemetry markers, hard-coded external URLs, and installer download, lookup, or online package-manager commands.
- Added a public privacy/network policy documenting every allowed connection, local logging behavior, offline installation, and clean-history publishing guidance.
- Bumped the privacy-hardened release identity to `v2.0.0-hotfix8-recovered.2`.
- Re-ran the complete Go test and vet suites after privacy hardening; all passed.
- Re-ran an independent source audit for license/telemetry markers, embedded external endpoints, online installer commands, and public-IP services; no matches remained.
- Rebuilt from the committed privacy branch with `-buildvcs=false` and reproduced the documented binary checksum.
- Prepared a privacy-clean delivery layout with a sanitized source snapshot, static binary, checksums, build metadata, and a fresh one-commit Git bundle that excludes the private recovery history.
- Added a table-driven transport matrix covering all 40 accepted protocol names and aliases.
- Passed real end-to-end TCP echo relay for all 40 transport entries.
- Passed real end-to-end UDP echo relay for all 40 transport entries with `accept_udp=true`.
- Restored the interactive Bash configuration, systemd, status, log, restart, and removal workflow around local recovered binaries only.
- Expanded the Bash transport menu to the same 40 names accepted by the Go runtime.
- Generated and parsed 80 Bash-managed configs: every transport in both server and client mode.
- Bumped the all-transport validated release identity to `v2.0.0-hotfix8-recovered.3`.
- Prepared the `.3` release layout with source, binary, Bash manager, validation reports, checksums, and a fresh one-commit public Git bundle.

## Remaining

- Push the validation branch if a writable GitHub repository becomes available.

## Last test result

- Input integrity check: PASS (`sha256sum` matched the recovered Library artifact).
- Bash manager syntax, help, privacy notice, and transport listing: PASS.
- Bash manager local verification (`--verify-local`): PASS, no files changed.
- Bash manager local install (`--install-local`): PASS; installed binary matched the source and reported the expected version.
- Baseline source import: complete.
- Source build: PASS with official Go 1.25.5 (`CGO_ENABLED=0 go build -buildvcs=false -trimpath`).
- Final reconstructed binary: static stripped linux/amd64 ELF, 13,631,672 bytes; SHA-256 `7cec15aacd8f16c78f7b0f1d003ad55ea5508faa1e12e7ef54fdb3e0e1818764`.
- Version check: PASS (`backhaul_recovered v2.0.0-hotfix8-recovered.3`).
- Focused TCP protocol tests: PASS (`TestNormalizeOriginalTarget`, `TestOriginalWireTarget`, and `TestOriginalTCPProtocolEndToEnd`).
- Original server / recovered client TCP interoperability: PASS, including a second relay after six seconds of heartbeat traffic.
- Recovered server / original client live relay: blocked by the retained original client, which also refuses to relay in an original-server/original-client localhost control test; reverse-direction wire frames were captured and matched instead.
- Full repository tests: PASS (`go test ./... -count=1`).
- Static analysis: PASS (`go vet ./...`).
- Privacy regression suite: PASS (`go test ./internal/privacy -count=1`).
- Privacy-hardened full suite: PASS (`go test ./... -count=1`).
- Privacy-hardened static analysis: PASS (`go vet ./...`).
- Independent forbidden-endpoint/source audit: PASS (no matches).
- All-transport TCP matrix: PASS (40/40).
- All-transport UDP matrix: PASS (40/40).
- Bash-generated configuration matrix: PASS (80/80).
- Final `.3` full repository suite and static analysis: PASS.
