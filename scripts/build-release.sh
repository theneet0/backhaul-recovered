#!/usr/bin/env bash
set -Eeuo pipefail

OUTPUT_DIR="${1:-dist}"
ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
mkdir -p "$OUTPUT_DIR"
OUTPUT_DIR="$(CDPATH= cd -- "$OUTPUT_DIR" && pwd -P)"

cd "$ROOT_DIR"
for arch in amd64 arm64; do
  echo "Building linux/${arch}..."
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -buildvcs=false -trimpath -ldflags="-s -w" \
    -o "${OUTPUT_DIR}/backhaul_linux_${arch}" ./cmd/backhaul
  chmod 0755 "${OUTPUT_DIR}/backhaul_linux_${arch}"
done

install -m 0755 installer/backhaul.sh "${OUTPUT_DIR}/backhaul.sh"
install -m 0755 install.sh "${OUTPUT_DIR}/install.sh"

(
  cd "$OUTPUT_DIR"
  sha256sum backhaul_linux_amd64 backhaul_linux_arm64 backhaul.sh install.sh > SHA256SUMS
)

echo "Release assets created in ${OUTPUT_DIR}:"
ls -lh "${OUTPUT_DIR}/backhaul_linux_amd64" \
       "${OUTPUT_DIR}/backhaul_linux_arm64" \
       "${OUTPUT_DIR}/backhaul.sh" \
       "${OUTPUT_DIR}/install.sh" \
       "${OUTPUT_DIR}/SHA256SUMS"
