#!/usr/bin/env bash
set -Eeuo pipefail

DEFAULT_GITHUB_REPO="theneet0/backhaul-recovered"
GITHUB_REPO="${BACKHAUL_GITHUB_REPO:-$DEFAULT_GITHUB_REPO}"
RELEASE_VERSION="${BACKHAUL_VERSION:-latest}"
INSTALL_DIR="${BACKHAUL_INSTALL_DIR:-/root/backhaul-core}"
COMMAND_PATH="${BACKHAUL_COMMAND_PATH:-/usr/local/bin/backhaul}"
RUN_MENU=true

usage() {
  cat <<USAGE
Backhaul GitHub quick installer

Usage: $0 [options]

Options:
  --repo OWNER/REPO    GitHub repository (default: $DEFAULT_GITHUB_REPO)
  --version TAG        Release tag to install (default: latest)
  --install-dir PATH   Installation directory (default: /root/backhaul-core)
  --command-path PATH  Manager command path (default: /usr/local/bin/backhaul)
  --no-menu            Install only; do not open the interactive manager
  -h, --help           Show this help

Environment variables:
  BACKHAUL_GITHUB_REPO
  BACKHAUL_VERSION
  BACKHAUL_INSTALL_DIR
  BACKHAUL_COMMAND_PATH
  BACKHAUL_RELEASE_BASE_URL  Override the release download base (testing/mirror)
USAGE
}

while (($#)); do
  case "$1" in
    --repo)
      [[ $# -ge 2 ]] || { echo "Missing value for --repo" >&2; exit 2; }
      GITHUB_REPO="$2"; shift 2 ;;
    --version)
      [[ $# -ge 2 ]] || { echo "Missing value for --version" >&2; exit 2; }
      RELEASE_VERSION="$2"; shift 2 ;;
    --install-dir)
      [[ $# -ge 2 ]] || { echo "Missing value for --install-dir" >&2; exit 2; }
      INSTALL_DIR="$2"; shift 2 ;;
    --command-path)
      [[ $# -ge 2 ]] || { echo "Missing value for --command-path" >&2; exit 2; }
      COMMAND_PATH="$2"; shift 2 ;;
    --no-menu)
      RUN_MENU=false; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 2 ;;
  esac
done

case "$GITHUB_REPO" in
  */*) ;;
  *) echo "Invalid GitHub repository: $GITHUB_REPO (expected OWNER/REPO)" >&2; exit 2 ;;
esac

case "$(uname -s)" in
  Linux) ;;
  *) echo "This installer currently supports Linux only." >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [[ $EUID -eq 0 ]]; then
  SUDO=()
elif command -v sudo >/dev/null 2>&1; then
  SUDO=(sudo)
else
  echo "Run as root or install sudo." >&2
  exit 1
fi

if command -v curl >/dev/null 2>&1; then
  download() { curl --fail --silent --show-error --location --retry 3 --connect-timeout 15 "$1" --output "$2"; }
elif command -v wget >/dev/null 2>&1; then
  download() { wget --quiet --tries=3 --timeout=15 --output-document="$2" "$1"; }
else
  echo "curl or wget is required." >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  verify_checksums() { sha256sum --check "$1"; }
elif command -v shasum >/dev/null 2>&1; then
  verify_checksums() { shasum -a 256 --check "$1"; }
else
  echo "sha256sum or shasum is required." >&2
  exit 1
fi

if [[ -n "${BACKHAUL_RELEASE_BASE_URL:-}" ]]; then
  RELEASE_BASE="${BACKHAUL_RELEASE_BASE_URL%/}"
elif [[ "$RELEASE_VERSION" == "latest" ]]; then
  RELEASE_BASE="https://github.com/${GITHUB_REPO}/releases/latest/download"
else
  RELEASE_BASE="https://github.com/${GITHUB_REPO}/releases/download/${RELEASE_VERSION}"
fi

BINARY_ASSET="backhaul_linux_${ARCH}"
MANAGER_ASSET="backhaul.sh"
CHECKSUM_ASSET="SHA256SUMS"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading Backhaul from ${GITHUB_REPO} (${RELEASE_VERSION}, linux/${ARCH})..."
download "${RELEASE_BASE}/${BINARY_ASSET}" "${TMP_DIR}/${BINARY_ASSET}"
download "${RELEASE_BASE}/${MANAGER_ASSET}" "${TMP_DIR}/${MANAGER_ASSET}"
download "${RELEASE_BASE}/${CHECKSUM_ASSET}" "${TMP_DIR}/${CHECKSUM_ASSET}"

awk -v binary="$BINARY_ASSET" -v manager="$MANAGER_ASSET" '
  $2 == binary || $2 == manager { print }
' "${TMP_DIR}/${CHECKSUM_ASSET}" > "${TMP_DIR}/SELECTED_SHA256SUMS"

if [[ "$(wc -l < "${TMP_DIR}/SELECTED_SHA256SUMS" | tr -d ' ')" != "2" ]]; then
  echo "Release checksum file does not contain both required assets." >&2
  exit 1
fi

(
  cd "$TMP_DIR"
  verify_checksums "SELECTED_SHA256SUMS"
)

chmod 0755 "${TMP_DIR}/${BINARY_ASSET}" "${TMP_DIR}/${MANAGER_ASSET}"
bash -n "${TMP_DIR}/${MANAGER_ASSET}"
VERSION_OUTPUT="$("${TMP_DIR}/${BINARY_ASSET}" -v 2>&1)"
case "$VERSION_OUTPUT" in
  backhaul_recovered\ v2.0.0-hotfix8-recovered.*) ;;
  *) echo "Unexpected binary identity: $VERSION_OUTPUT" >&2; exit 1 ;;
esac

"${SUDO[@]}" mkdir -p "$INSTALL_DIR" "$(dirname "$COMMAND_PATH")"
"${SUDO[@]}" install -m 0755 "${TMP_DIR}/${BINARY_ASSET}" "${INSTALL_DIR}/backhaul_premium"
"${SUDO[@]}" install -m 0755 "${TMP_DIR}/${MANAGER_ASSET}" "${INSTALL_DIR}/backhaul.sh"

WRAPPER="${TMP_DIR}/backhaul-wrapper"
cat > "$WRAPPER" <<EOF_WRAPPER
#!/usr/bin/env bash
exec "${INSTALL_DIR}/backhaul.sh" "\$@"
EOF_WRAPPER
chmod 0755 "$WRAPPER"
"${SUDO[@]}" install -m 0755 "$WRAPPER" "$COMMAND_PATH"

METADATA="${TMP_DIR}/INSTALL_SOURCE"
cat > "$METADATA" <<EOF_METADATA
repository=${GITHUB_REPO}
requested_release=${RELEASE_VERSION}
asset=${BINARY_ASSET}
installed_version=${VERSION_OUTPUT}
installed_at_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF_METADATA
"${SUDO[@]}" install -m 0644 "$METADATA" "${INSTALL_DIR}/INSTALL_SOURCE"

echo
echo "Installed successfully: ${VERSION_OUTPUT}"
echo "Core: ${INSTALL_DIR}/backhaul_premium"
echo "Manager: ${COMMAND_PATH}"
echo "The installer contacted GitHub only to download release assets; no telemetry was enabled."

if [[ "$RUN_MENU" == "true" ]]; then
  if [[ -r /dev/tty && -w /dev/tty ]]; then
    exec "${SUDO[@]}" "${INSTALL_DIR}/backhaul.sh" </dev/tty >/dev/tty
  fi
  echo "No interactive terminal detected. Run '${COMMAND_PATH}' to open the manager."
fi
