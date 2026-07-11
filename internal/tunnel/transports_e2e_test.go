package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var allTransportNames = []string{
	"tcp",
	"kcp",
	"kcpmux", "xkcpmux",
	"grpc", "grpcs",
	"grpcmux", "xgrpcmux",
	"grpcsmux", "xgrpcsmux",
	"http", "https",
	"httpmux", "xhttpmux",
	"httpsmux", "xhttpsmux",
	"udp",
	"udpmux", "xudpmux",
	"dns",
	"dnsmux", "xdnsmux",
	"slipstream", "slip", "sstream",
	"slipstreammux", "slipmux", "sstreammux",
	"raw", "rawsocket", "socketraw",
	"tun",
	"anytls",
	"tcpmux", "xtcpmux",
	"ws", "wss",
	"wsmux", "xwsmux", "wssmux",
}

func TestEveryTransportTCPRelay(t *testing.T) {
	certPath, keyPath := writeTransportTestCertificate(t)
	for _, transport := range allTransportNames {
		transport := transport
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			testTransportTCPRelay(t, transport, certPath, keyPath)
		})
	}
}

func TestEveryTransportUDPRelay(t *testing.T) {
	certPath, keyPath := writeTransportTestCertificate(t)
	for _, transport := range allTransportNames {
		transport := transport
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			testTransportUDPRelay(t, transport, certPath, keyPath)
		})
	}
}

func testTransportTCPRelay(t *testing.T, transport, certPath, keyPath string) {
	t.Helper()
	kind, err := parseTransport(transport)
	if err != nil {
		t.Fatal(err)
	}

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoListener.Close()
	go serveTransportTestEcho(echoListener)

	controlAddr := reserveTransportControlAddr(t, kind)
	publicAddr := reserveTCPAddr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	logger := log.New(io.Discard, "", 0)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- RunServer(ctx, ServerOpts{
			BindAddr:          controlAddr,
			Transport:         transport,
			Token:             "transport-matrix-token",
			Mappings:          []PortMapping{{ListenAddr: publicAddr, TargetAddr: echoListener.Addr().String()}},
			NoDelay:           true,
			Keepalive:         time.Second,
			HeartbeatInterval: 100 * time.Millisecond,
			HeartbeatTimeout:  2 * time.Second,
			TLSCert:           certPath,
			TLSKey:            keyPath,
			Logger:            logger,
		})
	}()

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- RunClient(ctx, ClientOpts{
			RemoteAddr:        controlAddr,
			Transport:         transport,
			Token:             "transport-matrix-token",
			DialTimeout:       time.Second,
			RetryInterval:     20 * time.Millisecond,
			PoolSize:          2,
			NoDelay:           true,
			Keepalive:         time.Second,
			HeartbeatInterval: 100 * time.Millisecond,
			HeartbeatTimeout:  2 * time.Second,
			TLSServerName:     "localhost",
			Logger:            logger,
		})
	}()

	payload := []byte("transport-matrix:" + transport)
	deadline := time.Now().Add(8 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-serverErr:
			if err != nil {
				t.Fatalf("server stopped before relay: %v", err)
			}
			t.Fatal("server stopped before relay")
		case err := <-clientErr:
			if err != nil {
				t.Fatalf("client stopped before relay: %v", err)
			}
			t.Fatal("client stopped before relay")
		default:
		}

		conn, err := net.DialTimeout("tcp", publicAddr, 250*time.Millisecond)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(750 * time.Millisecond))
			if _, err = conn.Write(payload); err == nil {
				got := make([]byte, len(payload))
				_, err = io.ReadFull(conn, got)
				if err == nil && bytes.Equal(got, payload) {
					_ = conn.Close()
					cancel()
					awaitTransportShutdown(t, "server", serverErr)
					awaitTransportShutdown(t, "client", clientErr)
					return
				}
			}
			_ = conn.Close()
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("relay did not pass traffic: %v", lastErr)
}

func testTransportUDPRelay(t *testing.T, transport, certPath, keyPath string) {
	t.Helper()
	kind, err := parseTransport(transport)
	if err != nil {
		t.Fatal(err)
	}

	echoConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer echoConn.Close()
	go serveTransportTestUDPEcho(echoConn)

	controlAddr := reserveTransportControlAddr(t, kind)
	publicAddr := reserveDualProtocolAddr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	logger := log.New(io.Discard, "", 0)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- RunServer(ctx, ServerOpts{
			BindAddr:          controlAddr,
			Transport:         transport,
			Token:             "transport-matrix-token",
			Mappings:          []PortMapping{{ListenAddr: publicAddr, TargetAddr: echoConn.LocalAddr().String()}},
			AcceptUDP:         true,
			NoDelay:           true,
			Keepalive:         time.Second,
			HeartbeatInterval: 100 * time.Millisecond,
			HeartbeatTimeout:  2 * time.Second,
			TLSCert:           certPath,
			TLSKey:            keyPath,
			Logger:            logger,
		})
	}()

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- RunClient(ctx, ClientOpts{
			RemoteAddr:        controlAddr,
			Transport:         transport,
			Token:             "transport-matrix-token",
			DialTimeout:       time.Second,
			RetryInterval:     20 * time.Millisecond,
			PoolSize:          2,
			NoDelay:           true,
			Keepalive:         time.Second,
			HeartbeatInterval: 100 * time.Millisecond,
			HeartbeatTimeout:  2 * time.Second,
			TLSServerName:     "localhost",
			Logger:            logger,
		})
	}()

	publicUDPAddr, err := net.ResolveUDPAddr("udp", publicAddr)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("transport-udp-matrix:" + transport)
	deadline := time.Now().Add(8 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-serverErr:
			if err != nil {
				t.Fatalf("server stopped before UDP relay: %v", err)
			}
			t.Fatal("server stopped before UDP relay")
		case err := <-clientErr:
			if err != nil {
				t.Fatalf("client stopped before UDP relay: %v", err)
			}
			t.Fatal("client stopped before UDP relay")
		default:
		}

		conn, err := net.DialUDP("udp", nil, publicUDPAddr)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(750 * time.Millisecond))
			if _, err = conn.Write(payload); err == nil {
				got := make([]byte, len(payload))
				var n int
				n, err = conn.Read(got)
				if err == nil && bytes.Equal(got[:n], payload) {
					_ = conn.Close()
					cancel()
					awaitTransportShutdown(t, "server", serverErr)
					awaitTransportShutdown(t, "client", clientErr)
					return
				}
			}
			_ = conn.Close()
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("UDP relay did not pass traffic: %v", lastErr)
}

func serveTransportTestEcho(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_, _ = io.Copy(c, c)
		}(conn)
	}
}

func serveTransportTestUDPEcho(conn *net.UDPConn) {
	buffer := make([]byte, 64*1024)
	for {
		n, addr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		_, _ = conn.WriteToUDP(buffer[:n], addr)
	}
}

func reserveTransportControlAddr(t *testing.T, kind transportKind) string {
	t.Helper()
	if kind.kcp || kind.udp {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		addr := conn.LocalAddr().String()
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		return addr
	}
	return reserveTCPAddr(t)
}

func reserveDualProtocolAddr(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := tcpListener.Addr().String()
		udpAddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			_ = tcpListener.Close()
			t.Fatal(err)
		}
		udpConn, err := net.ListenUDP("udp", udpAddr)
		if err == nil {
			_ = udpConn.Close()
			_ = tcpListener.Close()
			return addr
		}
		_ = tcpListener.Close()
	}
	t.Fatal("could not reserve a TCP/UDP port pair")
	return ""
}

func awaitTransportShutdown(t *testing.T, component string, errCh <-chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("%s shutdown: %v", component, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not stop", component)
	}
}

func writeTransportTestCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificate, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certPath := filepath.Join(directory, "cert.pem")
	keyPath := filepath.Join(directory, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
