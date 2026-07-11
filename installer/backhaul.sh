#!/usr/bin/env bash

SCRIPT_VERSION="v1.1.0-privacy-local"
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
service_dir="${BACKHAUL_SERVICE_DIR:-/etc/systemd/system}"
config_dir="${BACKHAUL_CONFIG_DIR:-/root/backhaul-core}"
CERT_DIR="$config_dir/cert_files"
CERT_FILE="$CERT_DIR/cert.crt"
KEY_FILE="$CERT_DIR/cert.key"
SERVER_IP="${BACKHAUL_SERVER_IP:-127.0.0.1}"
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
colorize() {
local color="$1"
local text="$2"
local style="${3:-normal}"
local black="\033[30m" red="\033[31m" green="\033[32m" yellow="\033[33m"
local blue="\033[34m" magenta="\033[35m" cyan="\033[36m" white="\033[37m"
local reset="\033[0m" normal="\033[0m" bold="\033[1m" underline="\033[4m"
local color_code
case $color in
black) color_code=$black ;; red) color_code=$red ;;
green) color_code=$green ;; yellow) color_code=$yellow ;;
blue) color_code=$blue ;; magenta) color_code=$magenta ;;
cyan) color_code=$cyan ;; white) color_code=$white ;;
*) color_code=$reset ;;
esac
local style_code
case $style in
bold) style_code=$bold ;; underline) style_code=$underline ;;
normal | *) style_code=$normal ;;
esac
echo -e "${style_code}${color_code}${text}${reset}"
}
require_root() {
if [[ $EUID -ne 0 ]]; then
echo "This operation must be run as root"
return 1
fi
}
initialize_manager() {
local detected_ip
require_root || exit 1
mkdir -p "$CERT_DIR"
if [[ -z "${BACKHAUL_SERVER_IP:-}" ]]; then
detected_ip=$(hostname -I 2>/dev/null | awk '{print $1}') || true
[[ -n "$detected_ip" ]] && SERVER_IP="$detected_ip"
fi
}
press_key() {
read -r -p "Press any key to continue..."
}
prompt_with_default() {
local prompt="$1"
local default="$2"
local var_name="$3"
local input
echo -ne "[-] $prompt (default: $default): "
read -r input
eval "$var_name=\"${input:-$default}\""
}
prompt_boolean() {
local prompt="$1"
local default="$2"
local var_name="$3"
while true; do
prompt_with_default "$prompt [true/false]" "$default" "$var_name"
local value="${!var_name}"
if [[ "$value" == "true" || "$value" == "false" ]]; then
break
fi
colorize red "Invalid input. Please enter 'true' or 'false'."
done
}
validate_cidr() {
local cidr="$1"
if [[ ! "$cidr" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]{1,2})$ ]]; then
return 1
fi
IFS='/' read -r ip mask <<< "$cidr"
IFS='.' read -r a b c d <<< "$ip"
if (( a<0 || a>255 || b<0 || b>255 || c<0 || c>255 || d<0 || d>255 )); then
return 1
fi
if (( mask < 1 || mask > 32 )); then
return 1
fi
local ip_int=$(( (a << 24) | (b << 16) | (c << 8) | d ))
local mask_int
if (( mask == 32 )); then
mask_int=0xFFFFFFFF
else
mask_int=$(( (0xFFFFFFFF << (32 - mask)) & 0xFFFFFFFF ))
fi
local net_int=$(( ip_int & mask_int ))
local broadcast_int=$(( net_int | (~mask_int & 0xFFFFFFFF) ))
if (( ip_int == net_int )); then
return 1
fi
if (( ip_int == broadcast_int )); then
return 1
fi
return 0
}
resolve_local_backhaul_binary() {
local arch binary_name candidate
arch=$(uname -m)
case "$arch" in
x86_64) binary_name="backhaul_recovered_linux_amd64" ;;
arm64|aarch64) binary_name="backhaul_recovered_linux_arm64" ;;
*) colorize red "Unsupported architecture: $arch"; return 1 ;;
esac
for candidate in \
"${BACKHAUL_BINARY:-}" \
"$SCRIPT_DIR/../dist/$binary_name" \
"$SCRIPT_DIR/../../bin/$binary_name" \
"$SCRIPT_DIR/../bin/$binary_name" \
"$PWD/$binary_name"
do
if [[ -n "$candidate" && -f "$candidate" && -x "$candidate" ]]; then
printf '%s\n' "$candidate"
return 0
fi
done
return 1
}
install_local_backhaul() {
local mode="${1:-startup}" source_binary version_output
if [[ -x "${config_dir}/backhaul_premium" && "$mode" != "menu" ]]; then
return 0
fi
source_binary=$(resolve_local_backhaul_binary) || {
colorize red "No local recovered binary found. Set BACKHAUL_BINARY=/absolute/path/to/binary."
return 1
}
version_output=$("$source_binary" -v 2>&1) || {
colorize red "Local binary version check failed: $source_binary"
return 1
}
if [[ "$version_output" != backhaul_recovered\ v2.0.0-hotfix8-recovered.* ]]; then
colorize red "Unexpected binary identity: $version_output"
return 1
fi
mkdir -p "$config_dir"
install -m 0755 -- "$source_binary" "${config_dir}/backhaul_premium"
colorize green "Local recovered core installed: $version_output" bold
colorize cyan "No download, update check, or host-information request was performed."
}
declare -A CONFIG
reset_config() {
CONFIG=()
}
prompt_connection_section() {
local mode="$1"  # server or client
colorize blue "━━━ Connection Configuration ━━━" bold
if [[ "$mode" == "server" ]]; then
prompt_with_default "Bind Address" ":8443" CONFIG[bind_addr]
if [[ -n "${CONFIG[bind_addr]}" && "${CONFIG[bind_addr]}" != *:* ]]; then
CONFIG[bind_addr]=":${CONFIG[bind_addr]}"
fi
else
while true; do
echo -ne "[*] IRAN Server Address [IP:Port] or [Domain:Port]: "
read -r CONFIG[remote_addr]
if [[ -z "${CONFIG[remote_addr]}" ]]; then
colorize red "Server address cannot be empty."
continue
fi
if [[ "${CONFIG[remote_addr]}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}:[0-9]{1,5}$ || \
"${CONFIG[remote_addr]}" =~ ^[a-zA-Z0-9.-]+:[0-9]{1,5}$ ]]; then
break
else
colorize red "Invalid format. Use IP:Port or Domain:Port."
fi
done
if [[ "${CONFIG[transport_type]}" == "ws" || "${CONFIG[transport_type]}" == "wss" || "${CONFIG[transport_type]}" == "wsmux" || "${CONFIG[transport_type]}" == "wssmux" || "${CONFIG[transport_type]}" == "xwsmux" ]]; then
echo -ne "[-] Edge IP/Domain (optional, press Enter to skip): "
read -r CONFIG[edge_ip]
fi
CONFIG[dial_timeout]="10"
CONFIG[retry_interval]="3"
fi
echo ""
}
prompt_security_section() {
colorize blue "━━━ Security Configuration ━━━" bold
prompt_with_default "Security Token" "your_token" CONFIG[token]
CONFIG[enable_encryption]="false"
echo ""
}
prompt_transport_section() {
local mode="$1"
local is_ipx="false"
colorize blue "━━━ Transport Configuration ━━━" bold
local valid_transports=("${SUPPORTED_TRANSPORTS[@]}")
echo "Available transports:"
printf '  • %s\n' "${valid_transports[@]}"
while true; do
echo -ne "Select transport: "
read -r CONFIG[transport_type]
[[ " ${valid_transports[*]} " =~ " ${CONFIG[transport_type]} " ]] && break
colorize red "Invalid transport."
done
if [[ "${CONFIG[transport_type]}" == "tun" ]]; then
echo
local encapsulations=(tcp ipx)
echo "Available encapsulations:"
printf '  • %s\n' "${encapsulations[@]}"
while true; do
echo -ne "Select encapsulation: "
read -r CONFIG[tun_encapsulation]
[[ " ${encapsulations[*]} " =~ " ${CONFIG[tun_encapsulation]} " ]] && break
colorize red "Invalid encapsulation."
done
fi
echo
if [[ "${CONFIG[tun_encapsulation]}" == "ipx" ]]; then
is_ipx="true"
fi
if [[ "$is_ipx" != "true" ]]; then
prompt_boolean "Enable TCP_NODELAY" "true" CONFIG[nodelay]
fi
if [[ "$mode" == "server" ]]; then
prompt_boolean "Accept UDP relay" "false" CONFIG[accept_udp]
if [[ ! "${CONFIG[transport_type]}" =~ ^(tun|ws)$ ]] && [[ "$is_ipx" != "true" ]]; then
prompt_boolean "Enable Proxy Protocol" "false" CONFIG[proxy_protocol]
fi
else
if [[ "${CONFIG[transport_type]}" != "tun" ]]; then
prompt_with_default "Connection Pool" "8" CONFIG[connection_pool]
fi
fi
CONFIG[heartbeat_interval]="10"
CONFIG[heartbeat_timeout]="25"
if [[ "$is_ipx" != "true" ]]; then
CONFIG[keepalive_period]="40"
fi
echo ""
}
prompt_mux_section() {
local transport="$1"
if [[ ! "$transport" =~ mux$ ]]; then
return
fi
colorize blue "━━━ Mux Configuration ━━━" bold
prompt_with_default "Mux Version [1 or 2]" "2" CONFIG[mux_version]
prompt_with_default "Mux Concurrency" "8" CONFIG[mux_concurrency]
CONFIG[mux_framesize]="32768"
CONFIG[mux_recievebuffer]="4194304"
CONFIG[mux_streambuffer]="2097152"
echo ""
}
prompt_tun_section() {
local transport="$1"
local mode="$2"
local is_ipx="$3"
[[ "$transport" != "tun" ]] && return
colorize blue "━━━ TUN Configuration ━━━" bold
prompt_with_default "TUN Device Name" "backhaul" CONFIG[tun_name]
local default_local default_remote
if [[ "$mode" == "server" ]]; then
default_local="10.10.10.1/24"
default_remote="10.10.10.2/24"
else
default_local="10.10.10.2/24"
default_remote="10.10.10.1/24"
fi
while true; do
prompt_with_default "TUN Local Address (CIDR)" "$default_local" CONFIG[tun_local_addr]
if validate_cidr "${CONFIG[tun_local_addr]}"; then
break
fi
local suggested=$(validate_cidr "${CONFIG[tun_local_addr]}" 2>&1)
colorize red "Invalid CIDR. Network address should be: $suggested"
done
while true; do
prompt_with_default "TUN Remote Address (CIDR)" "$default_remote" CONFIG[tun_remote_addr]
if validate_cidr "${CONFIG[tun_remote_addr]}"; then
break
fi
colorize red "Invalid CIDR format."
done
prompt_with_default "Health Port" "1234" CONFIG[tun_health_port]
if [[ "$is_ipx" == "true" ]]; then
prompt_with_default "MTU" "1320" CONFIG[tun_mtu]
else
prompt_with_default "MTU" "1500" CONFIG[tun_mtu]
fi
echo ""
}
prompt_tls_section() {
local mode="$1"
local transport="$2"
if [[ ! "$transport" =~ ^(anytls|wss|wssmux|grpcs|grpcsmux|xgrpcsmux|https|httpsmux|xhttpsmux)$ ]]; then
return
fi
colorize blue "━━━ TLS Configuration ━━━" bold
if [[ "$mode" == "client" ]]; then
local default_sni="${CONFIG[remote_addr]%%:*}"
prompt_with_default "TLS Server Name (SNI)" "$default_sni" CONFIG[tls_sni]
echo
return
fi
if [[ ! -f "$CERT_FILE" || ! -f "$KEY_FILE" ]]; then
colorize red "[*] TLS certificate or key missing, generating self-signed Ed25519 cert..."
openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -x509 -days 365 -sha256 -keyout "$KEY_FILE" -out  "$CERT_FILE" -subj "/CN=backhaul.com"
colorize green "[*] Generated $CERT_FILE and $KEY_FILE"
echo
fi
prompt_with_default "TLS Certificate Path" "$CERT_FILE" CONFIG[tls_cert]
prompt_with_default "TLS Key Path" "$KEY_FILE" CONFIG[tls_key]
echo ""
}
prompt_tuning_section() {
local is_ipx="$1"
local is_tun="$2"
colorize blue "━━━ Tuning Configuration ━━━" bold
prompt_boolean "Enable Auto Tuning" "true" CONFIG[auto_tuning]
echo
colorize magenta "Profiles: balanced, fast, latency, resource" normal
prompt_with_default "Kernel Tuning Profile" "balanced" CONFIG[tuning_profile]
prompt_with_default "Workers (0 = auto)" "0" CONFIG[workers]
if [[ "$is_tun" != "true" ]]; then
prompt_with_default "Channel Size" "4096" CONFIG[channel_size]
fi
if [[ "$is_tun" == "true" ]]; then
CONFIG[channel_size]="10_000"
fi
if [[ "$is_ipx" == "true" ]]; then
prompt_with_default "Batch Size" "2048" CONFIG[batch_size]
prompt_with_default "SO_SNDBUF (0 = auto)" "0" CONFIG[so_sndbuf]
else
prompt_with_default "TCP MSS (0 = auto)" "0" CONFIG[tcp_mss]
prompt_with_default "SO_RCVBUF (0 = auto)" "0" CONFIG[so_rcvbuf]
prompt_with_default "SO_SNDBUF (0 = auto)" "0" CONFIG[so_sndbuf]
fi
if [[ "$is_tun" != "true" ]] && [[ "$is_ipx" != "true" ]]; then
echo
colorize magenta "Buffer Profiles: extreme_low_cpu, ultra_low_cpu, low_cpu, balanced, low_memory" normal
prompt_with_default "Buffer Profile" "balanced" CONFIG[buffer_profile]
prompt_with_default "Read Timeout" "120" CONFIG[read_timeout]
fi
echo ""
}
prompt_logging_section() {
colorize blue "━━━ Logging Configuration ━━━" bold
colorize magenta "Levels: panic, fatal, error, warn, info, debug, trace"
prompt_with_default "Log Level" "info" CONFIG[log_level]
echo ""
}
prompt_accept_udp_section() {
local accept_udp="$1"
[[ "$accept_udp" != "true" ]] && return
CONFIG[ring_size]="64"
CONFIG[frame_size]="2048"
CONFIG[peer_idle_timeout_s]="120"
CONFIG[write_timeout_ms]="3"
}
prompt_ports_section() {
local mode="$1"
local is_tun="$2"
[[ "$mode" != "server" ]] && return
if [[ "$is_tun" != "true" ]]; then
colorize blue "━━━ Port Mapping Configuration ━━━" bold
colorize green "Supported formats:"
echo "  1. 443           - Listen on 443, forward to 443"
echo "  2. 443=5000      - Listen on 443, forward to 5000"
echo "  3. 443-600       - Listen on range 443-600"
echo "  4. 443-600:5201  - Range forwarding to 5201"
echo ""
echo -ne "Enter port mappings (comma-separated): "
read -r CONFIG[ports_mapping]
echo ""
else
colorize blue "━━━ Port Mapping Configuration (tun helper) ━━━" bold
colorize magenta "Forwarder: use 'bbackhaul' for TCP support only, or 'iptables' for TCP + UDP support"
prompt_with_default "Forwarder (backhaul/iptables)" "backhaul" CONFIG[forwarder]
echo ""
colorize green "Supported formats:"
echo "  1. 443           - Listen on 443, forward to 443"
echo "  2. 443=5000      - Listen on 443, forward to 5000"
echo ""
echo -ne "Enter port mappings (comma-separated): "
read -r CONFIG[ports_mapping]
echo ""
fi
}
prompt_ipx_section() {
local is_ipx="$1"
local mode="$2"
[[ "$is_ipx" != "true" ]] && return
colorize blue "━━━ IPX Configuration ━━━" bold
CONFIG[ipx_mode]="$mode"
AVAILABLE_PROFILES=("icmp" "ipip" "udp" "tcp" "gre" "bip")
colorize magenta "Available profiles: ${AVAILABLE_PROFILES[*]}"
while true; do
prompt_with_default "Profile" "tcp" CONFIG[ipx_profile]
CONFIG[ipx_profile]="${CONFIG[ipx_profile],,}"
for profile in "${AVAILABLE_PROFILES[@]}"; do
if [[ "${CONFIG[ipx_profile]}" == "$profile" ]]; then
break 2
fi
done
colorize red "Invalid profile: ${CONFIG[ipx_profile]}"
echo
colorize yellow "Please choose one of: ${AVAILABLE_PROFILES[*]}"
done
prompt_with_default "Listen IP" $SERVER_IP CONFIG[ipx_listen_ip]
while :; do
prompt_with_default "Destination IP" "" CONFIG[ipx_dst_ip]
if [[ -n "${CONFIG[ipx_dst_ip]}" ]]; then
break
fi
colorize red "Destination IP cannot be empty."
done
interface=$(ip route show default | awk '{print $5}')
prompt_with_default "Network Interface" $interface CONFIG[ipx_interface]
if [[ "${CONFIG[ipx_profile]}" == "icmp" ]]; then
prompt_with_default "ICMP Type" "0" CONFIG[ipx_icmp_type]
prompt_with_default "ICMP Code" "0" CONFIG[ipx_icmp_code]
fi
echo ""
}
generate_toml_config() {
local mode="$1"
local output_file="$2"
local is_tun="$3"
local is_ipx="$4"
{
if [[ "$mode" == "server" ]] && [[ "$is_ipx" == "false" ]]; then
echo "[listener]"
echo "bind_addr = \"${CONFIG[bind_addr]}\""
echo ""
elif [[ "$is_ipx" == "false" ]]; then
echo "[dialer]"
echo "remote_addr = \"${CONFIG[remote_addr]}\""
[[ -n "${CONFIG[edge_ip]}" ]] && echo "edge_ip = \"${CONFIG[edge_ip]}\""
echo "dial_timeout = ${CONFIG[dial_timeout]}"
echo "retry_interval = ${CONFIG[retry_interval]}"
echo ""
fi
echo "[transport]"
echo "type = \"${CONFIG[transport_type]}\""
[[ -n "${CONFIG[nodelay]}" ]] && echo "nodelay = ${CONFIG[nodelay]}"
[[ -n "${CONFIG[keepalive_period]}" ]] && echo "keepalive_period = ${CONFIG[keepalive_period]}"
if [[ "$mode" == "server" ]]; then
[[ -n "${CONFIG[accept_udp]}" ]] && echo "accept_udp = ${CONFIG[accept_udp]}"
[[ -n "${CONFIG[proxy_protocol]}" ]] && echo "proxy_protocol = ${CONFIG[proxy_protocol]}"
else
[[ -n "${CONFIG[connection_pool]}" ]] && [[ "${CONFIG[connection_pool]}" != "0" ]] && \
echo "connection_pool = ${CONFIG[connection_pool]}"
fi
[[ -n "${CONFIG[heartbeat_interval]}" ]] && echo "heartbeat_interval = ${CONFIG[heartbeat_interval]}"
[[ -n "${CONFIG[heartbeat_timeout]}" ]] && echo "heartbeat_timeout = ${CONFIG[heartbeat_timeout]}"
echo ""
if [[ "$is_tun" == "true" ]]; then
echo "[tun]"
echo "encapsulation = \"${CONFIG[tun_encapsulation]}\""
echo "name = \"${CONFIG[tun_name]}\""
echo "local_addr = \"${CONFIG[tun_local_addr]}\""
echo "remote_addr = \"${CONFIG[tun_remote_addr]}\""
echo "health_port = ${CONFIG[tun_health_port]}"
echo "mtu = ${CONFIG[tun_mtu]}"
echo ""
fi
if [[ "$is_ipx" == "true" ]]; then
echo "[ipx]"
echo "mode = \"${CONFIG[ipx_mode]}\""
echo "profile = \"${CONFIG[ipx_profile]}\""
echo "listen_ip = \"${CONFIG[ipx_listen_ip]}\""
echo "dst_ip = \"${CONFIG[ipx_dst_ip]}\""
echo "interface = \"${CONFIG[ipx_interface]}\""
[[ -n "${CONFIG[ipx_icmp_type]}" ]] && echo "icmp_type = ${CONFIG[ipx_icmp_type]}"
[[ -n "${CONFIG[ipx_icmp_code]}" ]] && echo "icmp_code = ${CONFIG[ipx_icmp_code]}"
echo ""
fi
if [[ "${CONFIG[transport_type]}" =~ mux$ ]]; then
echo "[mux]"
echo "mux_version = ${CONFIG[mux_version]}"
echo "mux_framesize = ${CONFIG[mux_framesize]}"
echo "mux_recievebuffer = ${CONFIG[mux_recievebuffer]}"
echo "mux_streambuffer = ${CONFIG[mux_streambuffer]}"
[[ -n "${CONFIG[mux_concurrency]}" ]] && echo "mux_concurrency = ${CONFIG[mux_concurrency]}"
echo ""
fi
echo "[security]"
echo "token = \"${CONFIG[token]}\""
echo "enable_encryption = false"
echo ""
if [[ -n "${CONFIG[tls_sni]}" || -n "${CONFIG[tls_cert]}" ]]; then
echo "[tls]"
[[ -n "${CONFIG[tls_sni]}" ]]  && echo "sni = \"${CONFIG[tls_sni]}\""
[[ -n "${CONFIG[tls_cert]}" ]] && echo "tls_cert = \"${CONFIG[tls_cert]}\""
[[ -n "${CONFIG[tls_key]}" ]]  && echo "tls_key = \"${CONFIG[tls_key]}\""
echo ""
fi
echo "[tuning]"
[[ -n "${CONFIG[auto_tuning]}" ]]     && echo "auto_tuning = ${CONFIG[auto_tuning]}"
[[ -n "${CONFIG[tuning_profile]}" ]]  && echo "tuning_profile = \"${CONFIG[tuning_profile]}\""
[[ -n "${CONFIG[workers]}" ]]         && echo "workers = ${CONFIG[workers]}"
[[ -n "${CONFIG[channel_size]}" ]]    && echo "channel_size = ${CONFIG[channel_size]}"
[[ -n "${CONFIG[tcp_mss]}" ]]         && echo "tcp_mss = ${CONFIG[tcp_mss]}"
[[ -n "${CONFIG[so_rcvbuf]}" ]]       && echo "so_rcvbuf = ${CONFIG[so_rcvbuf]}"
[[ -n "${CONFIG[so_sndbuf]}" ]]       && echo "so_sndbuf = ${CONFIG[so_sndbuf]}"
[[ -n "${CONFIG[buffer_profile]}" ]]  && echo "buffer_profile = \"${CONFIG[buffer_profile]}\""
[[ -n "${CONFIG[batch_size]}" ]]      && echo "batch_size = ${CONFIG[batch_size]}"
[[ -n "${CONFIG[read_timeout]}" ]]    && echo "read_timeout = ${CONFIG[read_timeout]}"
echo ""
if [[ "${CONFIG[accept_udp]}" == "true" ]]; then
echo "[accept_udp]"
echo "ring_size = ${CONFIG[ring_size]}"
echo "frame_size = ${CONFIG[frame_size]}"
echo "peer_idle_timeout_s = ${CONFIG[peer_idle_timeout_s]}"
echo "write_timeout_ms = ${CONFIG[write_timeout_ms]}"
echo ""
fi
echo "[logging]"
echo "log_level = \"${CONFIG[log_level]}\""
echo ""
if [[ "$mode" == "server" ]] ; then
echo "[ports]"
[[ -n "${CONFIG[forwarder]}" ]]  && echo "forwarder = \"${CONFIG[forwarder]}\""
echo "mapping = ["
IFS=',' read -r -a ports <<< "${CONFIG[ports_mapping]}"
for port in "${ports[@]}"; do
[[ -n "$port" ]] && echo "    \"${port// /}\","
done
echo "]"
fi
} > "$output_file"
}
configure_server() {
local mode="$1"  # server or client
local mode_name
if [[ "$mode" == "server" ]]; then
mode_name="IRAN (Server)"
else
mode_name="KHAREJ (Client)"
fi
clear
colorize cyan "Configuring $mode_name" bold
echo ""
reset_config
prompt_transport_section "$mode"
local is_tun="false"
local is_ipx="false"
[[ "${CONFIG[transport_type]}" == "tun" ]] && is_tun="true"
[[ "${CONFIG[tun_encapsulation]}" == "ipx" ]] && is_ipx="true"
prompt_tun_section "${CONFIG[transport_type]}" "$mode" "$is_ipx"
prompt_ipx_section "$is_ipx" "$mode"
if [[ "$is_ipx" != "true" ]]; then
prompt_connection_section "$mode"
fi
prompt_security_section "$is_ipx"
prompt_accept_udp_section "${CONFIG[accept_udp]}"
prompt_mux_section "${CONFIG[transport_type]}"
prompt_tls_section "$mode" "${CONFIG[transport_type]}"
prompt_tuning_section "$is_ipx" "$is_tun"
prompt_logging_section
prompt_ports_section "$mode" "$is_tun"
local tunnel_port
if [[ "$mode" == "server" ]]; then
tunnel_port=$(echo "${CONFIG[bind_addr]}" | grep -oP ':\K[0-9]+$')
else
tunnel_port=$(echo "${CONFIG[remote_addr]}" | grep -oP ':\K[0-9]+$')
fi
if [[ -z "$tunnel_port" ]]; then
tunnel_port=$(echo "${CONFIG[tun_health_port]}")
fi
local config_file
if [[ "$mode" == "server" ]]; then
config_file="${config_dir}/iran${tunnel_port}.toml"
else
config_file="${config_dir}/kharej${tunnel_port}.toml"
fi
generate_toml_config "$mode" "$config_file" "$is_tun" "$is_ipx"
local service_type
[[ "$mode" == "server" ]] && service_type="iran" || service_type="kharej"
create_systemd_service "$service_type" "$tunnel_port" "$config_file"
echo ""
colorize green "✔ Configuration completed successfully!" bold
echo ""
press_key
}
create_systemd_service() {
local type="$1"
local port="$2"
local config_file="$3"
local service_file="${service_dir}/backhaul-${type}${port}.service"
local desc_type="$(tr '[:lower:]' '[:upper:]' <<< "${type:0:1}")${type:1}"
cat > "$service_file" <<EOF
[Unit]
Description=Backhaul $desc_type Port $port
After=network.target
[Service]
Type=simple
User=root
ExecStart=${config_dir}/backhaul_premium -c $config_file
Restart=always
RestartSec=3
LimitNOFILE=1048576
TasksMax=infinity
LimitMEMLOCK=infinity
StandardOutput=journal
StandardError=journal
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now "backhaul-${type}${port}.service" >/dev/null 2>&1
colorize green "✔ Service backhaul-${type}${port} created and started" bold
}
display_logo() {
echo -e "\033[36m"
cat << "EOF"
▗▄▄▖  ▗▄▖  ▗▄▄▖▗▖ ▗▖▗▖ ▗▖ ▗▄▖ ▗▖ ▗▖▗▖
▐▌ ▐▌▐▌ ▐▌▐▌   ▐▌▗▞▘▐▌ ▐▌▐▌ ▐▌▐▌ ▐▌▐▌
▐▛▀▚▖▐▛▀▜▌▐▌   ▐▛▚▖ ▐▛▀▜▌▐▛▀▜▌▐▌ ▐▌▐▌
▐▙▄▞▘▐▌ ▐▌▝▚▄▄▖▐▌ ▐▌▐▌ ▐▌▐▌ ▐▌▝▚▄▞▘▐▙▄▄▖
Lightning-fast reverse tunneling solution
EOF
echo -e "\033[0m\033[32m"
echo -e "Script Version: \033[33m${SCRIPT_VERSION}\033[32m"
[[ -f "${config_dir}/backhaul_premium" ]] && \
echo -e "Core Version: \033[33m$($config_dir/backhaul_premium -v)\033[32m"
echo -e "Privacy Mode: \033[33mLocal-only installer; no telemetry or update requests\033[0m"
}
display_server_info() {
echo -e "\e[93m═══════════════════════════════════════════\e[0m"
echo -e "\033[36mIP Address:\033[0m $SERVER_IP"
}
display_backhaul_core_status() {
if [[ -f "${config_dir}/backhaul_premium" ]]; then
echo -e "\033[36mBackhaul Core:\033[0m \033[32mInstalled\033[0m"
else
echo -e "\033[36mBackhaul Core:\033[0m \033[31mNot installed\033[0m"
fi
echo -e "\e[93m═══════════════════════════════════════════\e[0m"
}
check_config_backup() {
missing_services=()
for config in "${config_dir}"/iran*.toml "${config_dir}"/kharej*.toml; do
[ -e "$config" ] || continue
fname=$(basename "$config")
if [[ "$fname" =~ ^(iran|kharej)([0-9]+)\.toml$ ]]; then
location="${BASH_REMATCH[1]}"
tunnel_port="${BASH_REMATCH[2]}"
service_file="${service_dir}/backhaul-${location}${tunnel_port}.service"
if [[ ! -f "$service_file" ]]; then
missing_services+=("$service_file:$location:$tunnel_port")
fi
fi
done
[[ ${#missing_services[@]} -eq 0 ]] && return 0
echo
colorize red "Missing service files:" bold
for entry in "${missing_services[@]}"; do
service_file="${entry%%:*}"
location="${entry#*:}"; location="${location%%:*}"
tunnel_port="${entry##*:}"
echo "- $service_file (type: $location, port: $tunnel_port)"
done
echo
read -r -p "Do you want to create missing service files? (y/n): " confirm
if [[ "$confirm" =~ ^[Yy]$ ]]; then
for entry in "${missing_services[@]}"; do
service_file="${entry%%:*}"
location="${entry#*:}"; location="${location%%:*}"
tunnel_port="${entry##*:}"
config_file="${config_dir}/${location}${tunnel_port}.toml"
desc_loc="$(tr '[:lower:]' '[:upper:]' <<< "${location:0:1}")${location:1}"
cat > "$service_file" <<EOF
[Unit]
Description=Backhaul $desc_loc Port $tunnel_port
After=network.target
[Service]
Type=simple
User=root
ExecStart=${config_dir}/backhaul_premium -c $config_file
Restart=always
RestartSec=3
LimitNOFILE=1048576
TasksMax=infinity
LimitMEMLOCK=infinity
StandardOutput=journal
StandardError=journal
[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable --now "$(basename "$service_file")"
echo "Created and started $(basename "$service_file")"
done
fi
sleep 2
}
check_config_backup
check_tunnel_status() {
if ! ls "$config_dir"/*.toml 1> /dev/null 2>&1; then
colorize red "No config files found." bold
press_key
return 1
fi
clear
colorize yellow "Checking all services status..." bold
sleep 1
echo
for config_path in "$config_dir"/{iran,kharej}*.toml; do
[ -f "$config_path" ] || continue
config_name=$(basename "$config_path")
config_name="${config_name%.toml}"
service_name="backhaul-${config_name}.service"
if [[ "$config_name" =~ ^iran([0-9]+)$ ]]; then
port="${BASH_REMATCH[1]}"
if systemctl is-active --quiet "$service_name"; then
colorize green "Iran service (port $port) is running"
else
colorize red "Iran service (port $port) is not running"
fi
elif [[ "$config_name" =~ ^kharej([0-9]+)$ ]]; then
port="${BASH_REMATCH[1]}"
if systemctl is-active --quiet "$service_name"; then
colorize green "Kharej service (port $port) is running"
else
colorize red "Kharej service (port $port) is not running"
fi
fi
done
echo
press_key
}
tunnel_management() {
if ! ls "$config_dir"/*.toml 1> /dev/null 2>&1; then
colorize red "No config files found." bold
press_key
return 1
fi
clear
colorize cyan "Existing services:" bold
echo
local index=1
declare -a configs
for config_path in "$config_dir"/{iran,kharej}*.toml; do
[ -f "$config_path" ] || continue
config_name=$(basename "$config_path")
if [[ "$config_name" =~ ^iran([0-9]+)\.toml$ ]]; then
port="${BASH_REMATCH[1]}"
configs+=("$config_path")
echo -e "\033[35m${index}\033[0m) \033[32mIran\033[0m (port: \033[33m$port\033[0m)"
((index++))
elif [[ "$config_name" =~ ^kharej([0-9]+)\.toml$ ]]; then
port="${BASH_REMATCH[1]}"
configs+=("$config_path")
echo -e "\033[35m${index}\033[0m) \033[32mKharej\033[0m (port: \033[33m$port\033[0m)"
((index++))
fi
done
echo
echo -ne "Enter your choice (0 to return): "
read -r choice
[[ "$choice" == "0" ]] && return
while ! [[ "$choice" =~ ^[0-9]+$ ]] || (( choice < 1 || choice > ${#configs[@]} )); do
colorize red "Invalid choice."
echo -ne "Enter your choice (0 to return): "
read -r choice
[[ "$choice" == "0" ]] && return
done
selected_config="${configs[$((choice - 1))]}"
config_name=$(basename "${selected_config%.toml}")
service_name="backhaul-${config_name}.service"
clear
colorize cyan "Manage $config_name:" bold
echo
colorize red "1) Remove this tunnel"
colorize yellow "2) Restart this tunnel"
echo "3) View service logs"
echo "4) View service status"
echo
read -r -p "Enter your choice (0 to return): " choice
case $choice in
1) destroy_tunnel "$selected_config" ;;
2) restart_service "$service_name" ;;
3) view_service_logs "$service_name" ;;
4) view_service_status "$service_name" ;;
0) return ;;
*) colorize red "Invalid option!" && sleep 1 ;;
esac
}
destroy_tunnel() {
config_path="$1"
config_name=$(basename "${config_path%.toml}")
service_name="backhaul-${config_name}.service"
service_path="$service_dir/$service_name"
[ -f "$config_path" ] && rm -f "$config_path"
if [[ -f "$service_path" ]]; then
systemctl is-active --quiet "$service_name" && systemctl disable --now "$service_name" >/dev/null 2>&1
rm -f "$service_path"
fi
systemctl daemon-reload
echo
colorize green "Tunnel destroyed successfully!" bold
echo
press_key
}
restart_service() {
echo
colorize yellow "Restarting $1" bold
if systemctl list-units --type=service | grep -q "$1"; then
systemctl restart "$1"
colorize green "Service restarted successfully" bold
echo
else
colorize red "Service not found"
fi
press_key
}
view_service_logs() {
clear
journalctl -eu "$1" -f -o cat
}
view_service_status() {
clear
systemctl status "$1"
press_key
}
remove_core() {
if find "$config_dir" -type f -name "*.toml" | grep -q .; then
colorize red "Delete all services first."
sleep 3
return 1
fi
colorize yellow "Remove Backhaul-Core? (y/n)"
read -r confirm
if [[ $confirm == [yY] ]]; then
[[ -d "$config_dir" ]] && rm -rf "$config_dir"
colorize green "Backhaul-Core removed." bold
fi
press_key
}
verify_local_backhaul() {
local source_binary version_output
source_binary=$(resolve_local_backhaul_binary) || {
colorize red "No local recovered binary found. Set BACKHAUL_BINARY=/absolute/path/to/binary."
return 1
}
version_output=$("$source_binary" -v 2>&1) || return 1
colorize green "Verified local build: $version_output" bold
if command -v sha256sum >/dev/null 2>&1; then
sha256sum "$source_binary"
fi
colorize cyan "No network request was made during verification."
}
show_privacy_info() {
colorize cyan "Privacy / offline policy" bold
echo " • The manager installs only a local recovered binary."
echo " • No core download, self-update, public-IP lookup, license call, or telemetry is performed."
echo " • Runtime connections are limited to peers and targets in generated TOML files."
echo " • Use BACKHAUL_BINARY=/path/to/binary to select a build explicitly."
}
configure_tunnel() {
[[ ! -x "${config_dir}/backhaul_premium" ]] && {
colorize red "Install Backhaul-Core first."
press_key
return 1
}
clear
echo ""
colorize green "1) Configure IRAN (Server)" bold
colorize magenta "2) Configure KHAREJ (Client)" bold
echo ""
read -r -p "Enter your choice: " configure_choice
case "$configure_choice" in
1) configure_server "server" ;;
2) configure_server "client" ;;
*) colorize red "Invalid option!" && sleep 1 ;;
esac
}
display_menu() {
clear
display_logo
display_server_info
display_backhaul_core_status
echo
colorize green " 1. Configure a new tunnel" bold
colorize red " 2. Tunnel management" bold
colorize cyan " 3. Check tunnel status" bold
echo " 4. Install/Update Core from local build"
echo " 5. Privacy and offline policy"
echo " 6. Remove Backhaul Core"
echo " 0. Exit"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}
read_option() {
read -r -p "Enter your choice [0-6]: " choice
case $choice in
1) configure_tunnel ;;
2) tunnel_management ;;
3) check_tunnel_status ;;
4) install_local_backhaul "menu"; press_key ;;
5) show_privacy_info; press_key ;;
6) remove_core ;;
0) exit 0 ;;
*) colorize red "Invalid option!" && sleep 1 ;;
esac
}
if [[ "${BACKHAUL_SOURCE_ONLY:-false}" == "true" ]]; then
return 0 2>/dev/null || exit 0
fi
case "${1:-}" in
--install-local)
install_local_backhaul "menu"
exit $?
;;
--verify-local)
verify_local_backhaul
exit $?
;;
--privacy)
show_privacy_info
exit 0
;;
--list-transports)
printf '%s\n' "${SUPPORTED_TRANSPORTS[@]}"
exit 0
;;
-h|--help)
echo "Usage: $0 [--install-local|--verify-local|--privacy|--list-transports]"
echo "Set BACKHAUL_BINARY to the local recovered binary path when needed."
exit 0
;;
"") initialize_manager ;;
*) colorize red "Unknown option: $1"; exit 2 ;;
esac
install_local_backhaul "startup" || true
while true; do
display_menu
read_option
done
