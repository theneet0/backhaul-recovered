# Worklog

## Recovery and publication

- Reconstructed a buildable Go project from retained behavioral evidence.
- Implemented and tested the measured original TCP framing and relay path.
- Added compatibility transports, configuration parsing, privacy regression tests,
  the local Bash manager, and GitHub Quick Install.
- Added CI and release workflows for Linux AMD64 and ARM64 builds.
- Published the source tree to `theneet0/backhaul-recovered`.

## Release procedure

The release workflow is available under **Actions → Release**. It runs the Go
and shell tests, builds both Linux binaries, creates `SHA256SUMS`, and uploads
all installer assets to the selected version tag.
