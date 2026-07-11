package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"open-backhaul/internal/cfg"
	"open-backhaul/internal/tunnel"
)

const Version = "v2.0.0-hotfix8-recovered.3"

func Run(configPath string) error {
	c, err := cfg.Load(configPath)
	if err != nil {
		return err
	}
	mode, err := c.Mode()
	if err != nil {
		return err
	}

	lg := log.New(os.Stdout, "[backhaul_recovered] ", log.LstdFlags)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	transport := normalizeTransportForRuntime(c)
	bindAddr := resolveServerBindAddr(c)
	remoteAddr := resolveClientRemoteAddr(c)
	maps := c.Ports.Mapping
	if len(maps) == 0 && (strings.EqualFold(c.Transport.Type, "tun") || c.IPX.Mode != "") && c.Tun.HealthPort > 0 {
		maps = []string{strconv.Itoa(c.Tun.HealthPort)}
	}

	if mode == "server" {
		parsedMappings, err := tunnel.ParseMappings(maps)
		if err != nil {
			return fmt.Errorf("ports mapping parse error: %w", err)
		}
		opts := tunnel.ServerOpts{
			BindAddr:          bindAddr,
			Transport:         transport,
			Token:             c.Security.Token,
			Mappings:          parsedMappings,
			AcceptUDP:         c.Transport.AcceptUDP,
			NoDelay:           c.Transport.NoDelay,
			Keepalive:         time.Duration(c.Transport.KeepalivePeriod) * time.Second,
			HeartbeatInterval: time.Duration(c.Transport.HeartbeatInterval) * time.Second,
			HeartbeatTimeout:  time.Duration(c.Transport.HeartbeatTimeout) * time.Second,
			TLSCert:           c.TLS.TLSCert,
			TLSKey:            c.TLS.TLSKey,
			Logger:            lg,
		}
		return tunnel.RunServer(ctx, opts)
	}

	opts := tunnel.ClientOpts{
		RemoteAddr:        remoteAddr,
		Transport:         transport,
		Token:             c.Security.Token,
		DialTimeout:       time.Duration(c.Dialer.DialTimeout) * time.Second,
		RetryInterval:     time.Duration(c.Dialer.RetryInterval) * time.Second,
		PoolSize:          c.Transport.ConnectionPool,
		NoDelay:           c.Transport.NoDelay,
		Keepalive:         time.Duration(c.Transport.KeepalivePeriod) * time.Second,
		HeartbeatInterval: time.Duration(c.Transport.HeartbeatInterval) * time.Second,
		HeartbeatTimeout:  time.Duration(c.Transport.HeartbeatTimeout) * time.Second,
		TLSServerName:     c.TLS.SNI,
		Logger:            lg,
	}
	return tunnel.RunClient(ctx, opts)
}

func normalizeTransportForRuntime(c *cfg.Config) string {
	t := strings.ToLower(strings.TrimSpace(c.Transport.Type))
	if t == "" {
		t = "tcp"
	}
	if t == "tun" {
		enc := strings.ToLower(strings.TrimSpace(c.Tun.Encapsulation))
		if enc == "slipstream" || enc == "slip" || enc == "sstream" {
			return "slipstreammux"
		}
		if enc == "ipx" || c.IPX.Mode != "" {
			return "xtcpmux"
		}
		return "tcpmux"
	}
	return t
}

func resolveServerBindAddr(c *cfg.Config) string {
	if strings.TrimSpace(c.Listener.BindAddr) != "" {
		return c.Listener.BindAddr
	}
	host := strings.TrimSpace(c.IPX.ListenIP)
	if host == "" {
		host = "0.0.0.0"
	}
	port := c.Tun.HealthPort
	if port <= 0 {
		port = 8443
	}
	return host + ":" + strconv.Itoa(port)
}

func resolveClientRemoteAddr(c *cfg.Config) string {
	if strings.TrimSpace(c.Dialer.RemoteAddr) != "" {
		return c.Dialer.RemoteAddr
	}
	host := strings.TrimSpace(c.IPX.DstIP)
	if host == "" {
		host = strings.TrimSpace(c.Tun.RemoteAddr)
		if strings.Contains(host, "/") {
			host = strings.SplitN(host, "/", 2)[0]
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}
	port := c.Tun.HealthPort
	if port <= 0 {
		port = 8443
	}
	return host + ":" + strconv.Itoa(port)
}
