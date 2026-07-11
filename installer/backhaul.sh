#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_VERSION="v1.1.0-privacy-local"
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
service_dir="${BACKHAUL_SERVICE_DIR:-/etc/systemd/system}"
config_dir="${BACKHAUL_CONFIG_DIR:-/root/backhaul-core}"
SUPPORTED_TRANSPORTS=(
  tcp
  kcp kcpmux xkcpmux
  grpc grpcs grpcmux xgrpcmux grpcsmux xgrpcsmux
  http https httpmux xhttpmux httpsmux xhttpsmux
  udp udpmux xudpmux
  dns dnsmux xdnsmux
  slipstream slip sstream slipstreammux slipmux sstreammux
  raw rawsocket socketraw
  tun anytls tcpmux xtcpmux
  ws wss wsmux xwsmux wssmux
)

declare -A CONFIG

colorize() {
  local color="$1" text="$2" style="${3:-normal}" code="0"
  case "$color" in
    red) code=31;; green) code=32;; yellow) code=33;; blue) code=34;;
    magenta) code=35;; cyan) code=36;; white) code=37;;
  esac
  [[ "$style" == "bold" ]] && printf '\033[1;%sm%s\033[0m\n' "$code" "$text" || printf '\033[%sm%s\033[0m\n' "$code" "$text"
}

require_root() {
  [[ $EUID -eq 0 ]] || { echo "This operation must be run as root." >&2; return 1; }
}

reset_config() { CONFIG=(); }

resolve_local_backhaul_binary() {
  local arch name candidate
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) name="backhaul_linux_amd64";;
    aarch64|arm64) name="backhaul_linux_arm64";;
    *) echo "Unsupported architecture: $arch" >&2; return 1;;
  esac
  for candidate in \
    "${BACKHAUL_BINARY:-}" \
    "$config_dir/backhaul_premium" \
    "$SCRIPT_DIR/../dist/$name" \
    "$PWD/$name"
  do
    [[ -n "$candidate" && -x "$candidate" ]] && { printf '%s\n' "$candidate"; return 0; }
  done
  return 1
}

verify_local_backhaul() {
  local binary version
  binary="$(resolve_local_backhaul_binary)" || { echo "Set BACKHAUL_BINARY=/absolute/path/to/binary" >&2; return 1; }
  version="$($binary -v 2>&1)" || return 1
  [[ "$version" == backhaul_recovered\ v2.0.0-hotfix8-recovered.* ]] || {
    echo "Unexpected binary identity: $version" >&2; return 1;
  }
  echo "Verified local build: $version"
  command -v sha256sum >/dev/null 2>&1 && sha256sum "$binary"
}

install_local_backhaul() {
  require_root || return 1
  local binary version
  binary="$(resolve_local_backhaul_binary)" || { echo "Set BACKHAUL_BINARY=/absolute/path/to/binary" >&2; return 1; }
  version="$($binary -v 2>&1)" || return 1
  [[ "$version" == backhaul_recovered\ v2.0.0-hotfix8-recovered.* ]] || {
    echo "Unexpected binary identity: $version" >&2; return 1;
  }
  mkdir -p "$config_dir"
  install -m 0755 "$binary" "$config_dir/backhaul_premium"
  echo "Installed: $version"
}

is_supported_transport() {
  local item
  for item in "${SUPPORTED_TRANSPORTS[@]}"; do [[ "$item" == "$1" ]] && return 0; done
  return 1
}

prompt_default() {
  local prompt="$1" default="$2" variable="$3" value
  read -r -p "$prompt [$default]: " value
  printf -v "$variable" '%s' "${value:-$default}"
}

generate_toml_config() {
  local mode="$1" output_file="$2" is_tun="${3:-false}" is_ipx="${4:-false}"
  mkdir -p "$(dirname "$output_file")"
  {
    if [[ "$mode" == "server" && "$is_ipx" == "false" ]]; then
      printf '[listener]\nbind_addr = "%s"\n\n' "${CONFIG[bind_addr]:-:8443}"
    elif [[ "$is_ipx" == "false" ]]; then
      printf '[dialer]\nremote_addr = "%s"\n' "${CONFIG[remote_addr]:-127.0.0.1:8443}"
      [[ -n "${CONFIG[edge_ip]:-}" ]] && printf 'edge_ip = "%s"\n' "${CONFIG[edge_ip]}"
      printf 'dial_timeout = %s\nretry_interval = %s\n\n' "${CONFIG[dial_timeout]:-10}" "${CONFIG[retry_interval]:-3}"
    fi

    printf '[transport]\ntype = "%s"\n' "${CONFIG[transport_type]:-tcp}"
    printf 'nodelay = %s\nkeepalive_period = %s\n' "${CONFIG[nodelay]:-true}" "${CONFIG[keepalive_period]:-40}"
    if [[ "$mode" == "server" ]]; then
      printf 'accept_udp = %s\nproxy_protocol = %s\n' "${CONFIG[accept_udp]:-false}" "${CONFIG[proxy_protocol]:-false}"
    else
      printf 'connection_pool = %s\n' "${CONFIG[connection_pool]:-8}"
    fi
    printf 'heartbeat_interval = %s\nheartbeat_timeout = %s\n\n' "${CONFIG[heartbeat_interval]:-10}" "${CONFIG[heartbeat_timeout]:-25}"

    if [[ "$is_tun" == "true" ]]; then
      printf '[tun]\nencapsulation = "%s"\nname = "%s"\nlocal_addr = "%s"\nremote_addr = "%s"\nhealth_port = %s\nmtu = %s\n\n' \
        "${CONFIG[tun_encapsulation]:-tcp}" "${CONFIG[tun_name]:-backhaul}" \
        "${CONFIG[tun_local_addr]:-10.10.10.1/24}" "${CONFIG[tun_remote_addr]:-10.10.10.2/24}" \
        "${CONFIG[tun_health_port]:-1234}" "${CONFIG[tun_mtu]:-1500}"
    fi

    if [[ "$is_ipx" == "true" ]]; then
      printf '[ipx]\nmode = "%s"\nprofile = "%s"\nlisten_ip = "%s"\ndst_ip = "%s"\ninterface = "%s"\n\n' \
        "$mode" "${CONFIG[ipx_profile]:-tcp}" "${CONFIG[ipx_listen_ip]:-0.0.0.0}" \
        "${CONFIG[ipx_dst_ip]:-127.0.0.1}" "${CONFIG[ipx_interface]:-eth0}"
    fi

    if [[ "${CONFIG[transport_type]:-}" == *mux ]]; then
      printf '[mux]\nmux_version = %s\nmux_concurrency = %s\nmux_framesize = %s\nmux_recievebuffer = %s\nmux_streambuffer = %s\n\n' \
        "${CONFIG[mux_version]:-2}" "${CONFIG[mux_concurrency]:-8}" "${CONFIG[mux_framesize]:-32768}" \
        "${CONFIG[mux_recievebuffer]:-4194304}" "${CONFIG[mux_streambuffer]:-2097152}"
    fi

    printf '[security]\ntoken = "%s"\nenable_encryption = false\n\n' "${CONFIG[token]:-your_token}"

    if [[ -n "${CONFIG[tls_sni]:-}${CONFIG[tls_cert]:-}" ]]; then
      printf '[tls]\n'
      [[ -n "${CONFIG[tls_sni]:-}" ]] && printf 'sni = "%s"\n' "${CONFIG[tls_sni]}"
      [[ -n "${CONFIG[tls_cert]:-}" ]] && printf 'tls_cert = "%s"\n' "${CONFIG[tls_cert]}"
      [[ -n "${CONFIG[tls_key]:-}" ]] && printf 'tls_key = "%s"\n' "${CONFIG[tls_key]}"
      printf '\n'
    fi

    printf '[tuning]\nauto_tuning = %s\ntuning_profile = "%s"\nworkers = %s\nchannel_size = %s\ntcp_mss = %s\nso_rcvbuf = %s\nso_sndbuf = %s\nbuffer_profile = "%s"\nread_timeout = %s\n\n' \
      "${CONFIG[auto_tuning]:-true}" "${CONFIG[tuning_profile]:-balanced}" "${CONFIG[workers]:-0}" \
      "${CONFIG[channel_size]:-4096}" "${CONFIG[tcp_mss]:-0}" "${CONFIG[so_rcvbuf]:-0}" \
      "${CONFIG[so_sndbuf]:-0}" "${CONFIG[buffer_profile]:-balanced}" "${CONFIG[read_timeout]:-120}"

    printf '[logging]\nlog_level = "%s"\n\n' "${CONFIG[log_level]:-info}"

    if [[ "$mode" == "server" ]]; then
      printf '[ports]\n'
      [[ -n "${CONFIG[forwarder]:-}" ]] && printf 'forwarder = "%s"\n' "${CONFIG[forwarder]}"
      printf 'mapping = [\n'
      local mapping
      IFS=',' read -r -a _mappings <<< "${CONFIG[ports_mapping]:-8080=8080}"
      for mapping in "${_mappings[@]}"; do
        mapping="${mapping// /}"
        [[ -n "$mapping" ]] && printf '    "%s",\n' "$mapping"
      done
      printf ']\n'
    fi
  } > "$output_file"
}

create_service() {
  require_root || return 1
  local name="$1" config="$2" service="$service_dir/backhaul-$name.service"
  cat > "$service" <<EOF
[Unit]
Description=Backhaul $name
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$config_dir/backhaul_premium -c $config
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now "backhaul-$name.service"
}

configure_tunnel() {
  require_root || return 1
  [[ -x "$config_dir/backhaul_premium" ]] || { echo "Install the core first." >&2; return 1; }
  local mode transport address token ports port config name is_tun=false is_ipx=false
  read -r -p "Mode [server/client]: " mode
  [[ "$mode" == "server" || "$mode" == "client" ]] || { echo "Invalid mode" >&2; return 1; }
  printf 'Transports: %s\n' "${SUPPORTED_TRANSPORTS[*]}"
  read -r -p "Transport: " transport
  is_supported_transport "$transport" || { echo "Unsupported transport" >&2; return 1; }
  prompt_default "Control address" "$([[ "$mode" == server ]] && echo :8443 || echo 127.0.0.1:8443)" address
  prompt_default "Security token" "change-this-token" token
  reset_config
  CONFIG[transport_type]="$transport"; CONFIG[token]="$token"
  CONFIG[nodelay]=true; CONFIG[keepalive_period]=40; CONFIG[heartbeat_interval]=10; CONFIG[heartbeat_timeout]=25
  if [[ "$mode" == "server" ]]; then
    CONFIG[bind_addr]="$address"; CONFIG[accept_udp]=false; CONFIG[proxy_protocol]=false
    prompt_default "Port mappings (comma separated)" "8080=8080" ports; CONFIG[ports_mapping]="$ports"
  else
    CONFIG[remote_addr]="$address"; CONFIG[dial_timeout]=10; CONFIG[retry_interval]=3; CONFIG[connection_pool]=8
  fi
  [[ "$transport" == "tun" ]] && is_tun=true
  port="${address##*:}"; [[ "$port" =~ ^[0-9]+$ ]] || port=8443
  name="$([[ "$mode" == server ]] && echo iran || echo kharej)$port"
  config="$config_dir/$name.toml"
  generate_toml_config "$mode" "$config" "$is_tun" "$is_ipx"
  create_service "$name" "$config"
  echo "Created $config and backhaul-$name.service"
}

manage_services() {
  require_root || return 1
  local action name
  systemctl list-unit-files 'backhaul-*.service' --no-legend 2>/dev/null || true
  read -r -p "Action [status/restart/logs/remove/back]: " action
  [[ "$action" == back ]] && return 0
  read -r -p "Service name without .service: " name
  case "$action" in
    status) systemctl status "$name.service";;
    restart) systemctl restart "$name.service";;
    logs) journalctl -u "$name.service" -n 100 --no-pager;;
    remove)
      systemctl disable --now "$name.service" 2>/dev/null || true
      rm -f "$service_dir/$name.service"
      rm -f "$config_dir/${name#backhaul-}.toml"
      systemctl daemon-reload;;
    *) echo "Invalid action" >&2;;
  esac
}

show_privacy_info() {
  cat <<'EOF'
Privacy policy:
- No license, analytics, telemetry, public-IP, ISP, or update service is contacted.
- The manager operates only on local files and systemd.
- Runtime traffic is limited to peers and targets configured in TOML.
EOF
}

main_menu() {
  while true; do
    printf '\nBackhaul manager %s\n1) Configure tunnel\n2) Manage services\n3) Install local core\n4) Verify local core\n5) Privacy\n0) Exit\n' "$SCRIPT_VERSION"
    read -r -p "Choice: " choice
    case "$choice" in
      1) configure_tunnel;; 2) manage_services;; 3) install_local_backhaul;;
      4) verify_local_backhaul;; 5) show_privacy_info;; 0) exit 0;;
      *) echo "Invalid choice";;
    esac
  done
}

if [[ "${BACKHAUL_SOURCE_ONLY:-false}" == "true" ]]; then
  return 0 2>/dev/null || exit 0
fi

case "${1:-}" in
  --list-transports) printf '%s\n' "${SUPPORTED_TRANSPORTS[@]}";;
  --verify-local) verify_local_backhaul;;
  --install-local) install_local_backhaul;;
  --privacy) show_privacy_info;;
  -h|--help)
    echo "Usage: $0 [--list-transports|--verify-local|--install-local|--privacy]";;
  "") main_menu;;
  *) echo "Unknown option: $1" >&2; exit 2;;
esac
