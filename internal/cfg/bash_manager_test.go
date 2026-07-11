package cfg_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"open-backhaul/internal/cfg"
	"open-backhaul/internal/tunnel"
)

func TestBashManagerGeneratesEveryTransportConfig(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	installer := filepath.Join(root, "installer", "backhaul.sh")
	output, err := exec.Command(installer, "--list-transports").Output()
	if err != nil {
		t.Fatalf("list transports: %v", err)
	}
	transports := strings.Fields(string(output))
	if len(transports) != 40 {
		t.Fatalf("manager returned %d transports, want 40: %v", len(transports), transports)
	}

	seen := make(map[string]bool, len(transports))
	for _, transport := range transports {
		if seen[transport] {
			t.Fatalf("duplicate transport in manager: %s", transport)
		}
		seen[transport] = true
		if err := tunnel.ValidateTransport(transport); err != nil {
			t.Fatalf("manager exposes unsupported transport %q: %v", transport, err)
		}
		for _, mode := range []string{"server", "client"} {
			mode := mode
			t.Run(transport+"/"+mode, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), transport+"-"+mode+".toml")
				renderBashManagerConfig(t, installer, transport, mode, path)
				config, err := cfg.Load(path)
				if err != nil {
					t.Fatalf("load generated config: %v", err)
				}
				gotMode, err := config.Mode()
				if err != nil {
					t.Fatalf("generated mode: %v", err)
				}
				if gotMode != mode {
					t.Fatalf("generated mode=%q want %q", gotMode, mode)
				}
				if config.Transport.Type != transport {
					t.Fatalf("generated transport=%q want %q", config.Transport.Type, transport)
				}
				if config.Security.Token != "bash-manager-test-token" {
					t.Fatalf("generated token was not preserved")
				}
				if mode == "server" {
					if _, err := tunnel.ParseMappings(config.Ports.Mapping); err != nil {
						t.Fatalf("generated mappings: %v", err)
					}
				}
			})
		}
	}
}

func renderBashManagerConfig(t *testing.T, installer, transport, mode, output string) {
	t.Helper()
	script := `
set -e
export BACKHAUL_SOURCE_ONLY=true
source "$TEST_INSTALLER"
reset_config
CONFIG[transport_type]="$TEST_TRANSPORT"
CONFIG[tun_encapsulation]="tcp"
CONFIG[nodelay]="true"
CONFIG[keepalive_period]="40"
CONFIG[accept_udp]="true"
CONFIG[proxy_protocol]="false"
CONFIG[connection_pool]="2"
CONFIG[heartbeat_interval]="1"
CONFIG[heartbeat_timeout]="5"
CONFIG[token]="bash-manager-test-token"
CONFIG[enable_encryption]="false"
CONFIG[auto_tuning]="false"
CONFIG[tuning_profile]="balanced"
CONFIG[workers]="1"
CONFIG[channel_size]="64"
CONFIG[tcp_mss]="0"
CONFIG[so_rcvbuf]="0"
CONFIG[so_sndbuf]="0"
CONFIG[buffer_profile]="balanced"
CONFIG[read_timeout]="30"
CONFIG[log_level]="info"
CONFIG[ports_mapping]="18080=18080"
CONFIG[ring_size]="64"
CONFIG[frame_size]="2048"
CONFIG[peer_idle_timeout_s]="120"
CONFIG[write_timeout_ms]="3"
CONFIG[bind_addr]="127.0.0.1:18443"
CONFIG[remote_addr]="127.0.0.1:18443"
CONFIG[dial_timeout]="2"
CONFIG[retry_interval]="1"
is_tun=false
if [[ "$TEST_TRANSPORT" == "tun" ]]; then
  is_tun=true
  CONFIG[tun_name]="backhaul-test"
  CONFIG[tun_local_addr]="10.10.10.1/24"
  CONFIG[tun_remote_addr]="10.10.10.2/24"
  CONFIG[tun_health_port]="1234"
  CONFIG[tun_mtu]="1500"
fi
if [[ "$TEST_TRANSPORT" == *mux ]]; then
  CONFIG[mux_version]="2"
  CONFIG[mux_concurrency]="2"
  CONFIG[mux_framesize]="32768"
  CONFIG[mux_recievebuffer]="4194304"
  CONFIG[mux_streambuffer]="2097152"
fi
if [[ "$TEST_TRANSPORT" =~ ^(anytls|wss|wssmux|grpcs|grpcsmux|xgrpcsmux|https|httpsmux|xhttpsmux)$ ]]; then
  CONFIG[tls_sni]="localhost"
  CONFIG[tls_cert]="/tmp/backhaul-test-cert.pem"
  CONFIG[tls_key]="/tmp/backhaul-test-key.pem"
fi
generate_toml_config "$TEST_MODE" "$TEST_OUTPUT" "$is_tun" "false"
`
	command := exec.Command("bash", "-c", script)
	command.Env = append(os.Environ(),
		"TEST_INSTALLER="+installer,
		"TEST_TRANSPORT="+transport,
		"TEST_MODE="+mode,
		"TEST_OUTPUT="+output,
		"BACKHAUL_CONFIG_DIR="+filepath.Join(t.TempDir(), "core"),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("render config: %v\n%s", err, output)
	}
}
