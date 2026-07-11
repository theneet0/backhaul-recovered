#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
RELEASE_DIR="$(mktemp -d)"
INSTALL_ROOT="$(mktemp -d)"
SERVER_PID=""
cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$RELEASE_DIR" "$INSTALL_ROOT"
}
trap cleanup EXIT

cat > "${RELEASE_DIR}/backhaul_linux_amd64" <<'CORE'
#!/usr/bin/env bash
if [[ "${1:-}" == "-v" ]]; then
  echo 'backhaul_recovered v2.0.0-hotfix8-recovered.3'
  exit 0
fi
echo 'mock backhaul core'
CORE
chmod 0755 "${RELEASE_DIR}/backhaul_linux_amd64"
install -m 0755 "${ROOT_DIR}/installer/backhaul.sh" "${RELEASE_DIR}/backhaul.sh"
(
  cd "$RELEASE_DIR"
  sha256sum backhaul_linux_amd64 backhaul.sh > SHA256SUMS
)

PORT="$(python3 - <<'PY'
import socket
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"
python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$RELEASE_DIR" \
  >"${INSTALL_ROOT}/http.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:${PORT}/SHA256SUMS" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl -fsS "http://127.0.0.1:${PORT}/SHA256SUMS" >/dev/null

BACKHAUL_RELEASE_BASE_URL="http://127.0.0.1:${PORT}" \
  "${ROOT_DIR}/install.sh" \
  --install-dir "${INSTALL_ROOT}/core" \
  --command-path "${INSTALL_ROOT}/bin/backhaul" \
  --no-menu

test -x "${INSTALL_ROOT}/core/backhaul_premium"
test -x "${INSTALL_ROOT}/core/backhaul.sh"
test -x "${INSTALL_ROOT}/bin/backhaul"
test "$("${INSTALL_ROOT}/core/backhaul_premium" -v)" = \
  'backhaul_recovered v2.0.0-hotfix8-recovered.3'
test "$("${INSTALL_ROOT}/bin/backhaul" --list-transports | wc -l | tr -d ' ')" = '40'
grep -q '^repository=theneet0/backhaul-recovered$' \
  "${INSTALL_ROOT}/core/INSTALL_SOURCE"

printf '\ncorrupted\n' >> "${RELEASE_DIR}/backhaul_linux_amd64"
if BACKHAUL_RELEASE_BASE_URL="http://127.0.0.1:${PORT}" \
  "${ROOT_DIR}/install.sh" \
  --install-dir "${INSTALL_ROOT}/tampered-core" \
  --command-path "${INSTALL_ROOT}/tampered-bin/backhaul" \
  --no-menu >/dev/null 2>&1; then
  echo 'tampered release asset was unexpectedly accepted' >&2
  exit 1
fi
test ! -e "${INSTALL_ROOT}/tampered-core/backhaul_premium"

echo 'quick install test: PASS'
