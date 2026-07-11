package cfg

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Listener  Listener
	Dialer    Dialer
	Transport Transport
	Ports     Ports
	Security  Security
	TLS       TLS
	Logging   Logging
	Mux       Mux
	Tun       Tun
	IPX       IPX
	Tuning    Tuning
	AcceptUDP AcceptUDP
}

type Listener struct{ BindAddr string }
type Dialer struct {
	RemoteAddr    string
	EdgeIP        string
	DialTimeout   int
	RetryInterval int
}
type Transport struct {
	Type              string
	ConnectionPool    int
	NoDelay           bool
	KeepalivePeriod   int
	AcceptUDP         bool
	ProxyProtocol     bool
	HeartbeatInterval int
	HeartbeatTimeout  int
}
type Ports struct {
	Forwarder string
	Mapping   []string
}
type Security struct {
	Token            string
	EnableEncryption bool
	Algorithm        string
	PSK              string
	KDFIterations    int
}
type TLS struct {
	SNI     string
	TLSCert string
	TLSKey  string
}
type Logging struct{ LogLevel string }
type Mux struct {
	Version       int
	Concurrency   int
	FrameSize     int
	ReceiveBuffer int
	StreamBuffer  int
}
type Tun struct {
	Encapsulation string
	Name          string
	LocalAddr     string
	RemoteAddr    string
	HealthPort    int
	MTU           int
}
type IPX struct {
	Mode      string
	Profile   string
	ListenIP  string
	DstIP     string
	Interface string
	ICMPType  int
	ICMPCode  int
}
type Tuning struct {
	AutoTuning    bool
	TuningProfile string
	Workers       int
	ChannelSize   int
	TCPMSS        int
	SORcvBuf      int
	SOSndBuf      int
	BufferProfile string
	BatchSize     int
	ReadTimeout   int
}
type AcceptUDP struct {
	RingSize        int
	FrameSize       int
	PeerIdleTimeout int
	WriteTimeoutMS  int
}

const defaultToken = "your_token"

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	c := &Config{}
	c.Transport.Type = "tcp"
	c.Transport.ConnectionPool = 8
	c.Transport.NoDelay = true
	c.Transport.KeepalivePeriod = 40
	c.Transport.HeartbeatInterval = 10
	c.Transport.HeartbeatTimeout = 25
	c.Dialer.DialTimeout = 10
	c.Dialer.RetryInterval = 3
	c.Security.Token = defaultToken
	c.Logging.LogLevel = "info"
	c.Mux.Version = 2
	c.Mux.Concurrency = 8
	c.Mux.FrameSize = 32768
	c.Mux.ReceiveBuffer = 4194304
	c.Mux.StreamBuffer = 2097152
	c.Tun.Name = "backhaul"
	c.Tun.HealthPort = 1234
	c.Tun.MTU = 1500
	c.Tuning.AutoTuning = true
	c.Tuning.TuningProfile = "balanced"
	c.Tuning.BufferProfile = "balanced"
	c.AcceptUDP.RingSize = 64
	c.AcceptUDP.FrameSize = 2048
	c.AcceptUDP.PeerIdleTimeout = 120
	c.AcceptUDP.WriteTimeoutMS = 3

	section := ""
	inMapping := false
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if inMapping {
			if strings.Contains(line, "]") {
				inMapping = false
				line = strings.TrimSpace(strings.TrimSuffix(line, "]"))
				if line == "" {
					continue
				}
			}
			item := strings.Trim(line, " ,\t\r\n\"")
			if item != "" {
				c.Ports.Mapping = append(c.Ports.Mapping, item)
			}
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		if section == "ports" && key == "mapping" {
			val = strings.TrimSpace(strings.TrimPrefix(val, "["))
			if strings.Contains(val, "]") {
				val = strings.TrimSpace(strings.TrimSuffix(val, "]"))
				if val != "" {
					for _, p := range strings.Split(val, ",") {
						p = strings.Trim(p, " \t\r\n\"")
						if p != "" {
							c.Ports.Mapping = append(c.Ports.Mapping, p)
						}
					}
				}
			} else {
				inMapping = true
				if val != "" {
					p := strings.Trim(val, " \t\r\n\"")
					if p != "" {
						c.Ports.Mapping = append(c.Ports.Mapping, p)
					}
				}
			}
			continue
		}
		setValue(c, section, key, unquote(val))
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return c, nil
}

func setValue(c *Config, section, key, val string) {
	atoi := func(v string, def int) int {
		n, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return n
	}
	switch section {
	case "listener":
		if key == "bind_addr" {
			c.Listener.BindAddr = val
		}
	case "dialer":
		switch key {
		case "remote_addr":
			c.Dialer.RemoteAddr = val
		case "edge_ip":
			c.Dialer.EdgeIP = val
		case "dial_timeout":
			c.Dialer.DialTimeout = atoi(val, c.Dialer.DialTimeout)
		case "retry_interval":
			c.Dialer.RetryInterval = atoi(val, c.Dialer.RetryInterval)
		}
	case "transport":
		switch key {
		case "type":
			c.Transport.Type = strings.ToLower(val)
		case "connection_pool":
			c.Transport.ConnectionPool = atoi(val, c.Transport.ConnectionPool)
		case "nodelay":
			c.Transport.NoDelay = atob(val, c.Transport.NoDelay)
		case "keepalive_period":
			c.Transport.KeepalivePeriod = atoi(val, c.Transport.KeepalivePeriod)
		case "accept_udp":
			c.Transport.AcceptUDP = atob(val, c.Transport.AcceptUDP)
		case "proxy_protocol":
			c.Transport.ProxyProtocol = atob(val, c.Transport.ProxyProtocol)
		case "heartbeat_interval":
			c.Transport.HeartbeatInterval = atoi(val, c.Transport.HeartbeatInterval)
		case "heartbeat_timeout":
			c.Transport.HeartbeatTimeout = atoi(val, c.Transport.HeartbeatTimeout)
		}
	case "ports":
		if key == "forwarder" {
			c.Ports.Forwarder = val
		}
	case "security":
		switch key {
		case "token":
			c.Security.Token = val
		case "enable_encryption":
			c.Security.EnableEncryption = atob(val, c.Security.EnableEncryption)
		case "algorithm":
			c.Security.Algorithm = val
		case "psk":
			c.Security.PSK = val
		case "kdf_iterations":
			c.Security.KDFIterations = atoi(val, c.Security.KDFIterations)
		}
	case "tls":
		switch key {
		case "sni":
			c.TLS.SNI = val
		case "tls_cert":
			c.TLS.TLSCert = val
		case "tls_key":
			c.TLS.TLSKey = val
		}
	case "logging":
		if key == "log_level" {
			c.Logging.LogLevel = strings.ToLower(val)
		}
	case "mux":
		switch key {
		case "mux_version":
			c.Mux.Version = atoi(val, c.Mux.Version)
		case "mux_concurrency":
			c.Mux.Concurrency = atoi(val, c.Mux.Concurrency)
		case "mux_framesize":
			c.Mux.FrameSize = atoi(val, c.Mux.FrameSize)
		case "mux_recievebuffer", "mux_receivebuffer":
			c.Mux.ReceiveBuffer = atoi(val, c.Mux.ReceiveBuffer)
		case "mux_streambuffer":
			c.Mux.StreamBuffer = atoi(val, c.Mux.StreamBuffer)
		}
	case "tun":
		switch key {
		case "encapsulation":
			c.Tun.Encapsulation = strings.ToLower(val)
		case "name":
			c.Tun.Name = val
		case "local_addr":
			c.Tun.LocalAddr = val
		case "remote_addr":
			c.Tun.RemoteAddr = val
		case "health_port":
			c.Tun.HealthPort = atoi(val, c.Tun.HealthPort)
		case "mtu":
			c.Tun.MTU = atoi(val, c.Tun.MTU)
		}
	case "ipx":
		switch key {
		case "mode":
			c.IPX.Mode = strings.ToLower(val)
		case "profile":
			c.IPX.Profile = strings.ToLower(val)
		case "listen_ip":
			c.IPX.ListenIP = val
		case "dst_ip":
			c.IPX.DstIP = val
		case "interface":
			c.IPX.Interface = val
		case "icmp_type":
			c.IPX.ICMPType = atoi(val, c.IPX.ICMPType)
		case "icmp_code":
			c.IPX.ICMPCode = atoi(val, c.IPX.ICMPCode)
		}
	case "tuning":
		switch key {
		case "auto_tuning":
			c.Tuning.AutoTuning = atob(val, c.Tuning.AutoTuning)
		case "tuning_profile":
			c.Tuning.TuningProfile = strings.ToLower(val)
		case "workers":
			c.Tuning.Workers = atoi(val, c.Tuning.Workers)
		case "channel_size":
			c.Tuning.ChannelSize = atoi(val, c.Tuning.ChannelSize)
		case "tcp_mss":
			c.Tuning.TCPMSS = atoi(val, c.Tuning.TCPMSS)
		case "so_rcvbuf":
			c.Tuning.SORcvBuf = atoi(val, c.Tuning.SORcvBuf)
		case "so_sndbuf":
			c.Tuning.SOSndBuf = atoi(val, c.Tuning.SOSndBuf)
		case "buffer_profile":
			c.Tuning.BufferProfile = strings.ToLower(val)
		case "batch_size":
			c.Tuning.BatchSize = atoi(val, c.Tuning.BatchSize)
		case "read_timeout":
			c.Tuning.ReadTimeout = atoi(val, c.Tuning.ReadTimeout)
		}
	case "accept_udp":
		switch key {
		case "ring_size":
			c.AcceptUDP.RingSize = atoi(val, c.AcceptUDP.RingSize)
		case "frame_size":
			c.AcceptUDP.FrameSize = atoi(val, c.AcceptUDP.FrameSize)
		case "peer_idle_timeout_s":
			c.AcceptUDP.PeerIdleTimeout = atoi(val, c.AcceptUDP.PeerIdleTimeout)
		case "write_timeout_ms":
			c.AcceptUDP.WriteTimeoutMS = atoi(val, c.AcceptUDP.WriteTimeoutMS)
		}
	}
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, "\"")
	return v
}

func atob(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func (c *Config) Mode() (string, error) {
	if c.Listener.BindAddr != "" {
		return "server", nil
	}
	if c.Dialer.RemoteAddr != "" {
		return "client", nil
	}
	if c.IPX.Mode == "server" || c.IPX.Mode == "client" {
		return c.IPX.Mode, nil
	}
	return "", fmt.Errorf("config must contain [listener].bind_addr or [dialer].remote_addr")
}
