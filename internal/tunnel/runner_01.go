package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kcp "github.com/xtaci/kcp-go/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
)

var (
	_ = bufio.NewReader
	_ = bytes.NewBuffer
	_ = context.Background
	_ = rand.Reader
	_ = sha1.New
	_ = sha256.New
	_ = tls.VersionTLS13
	_ = base64.StdEncoding
	_ = binary.BigEndian
	_ = json.Marshal
	_ = errors.New
	_ = fmt.Sprintf
	_ = io.EOF
	_ = log.Default
	_ = net.IPv4len
	_ = http.MethodGet
	_ = url.URL{}
	_ = strings.TrimSpace
	_ = sync.Mutex{}
	_ = atomic.Bool{}
	_ = time.Second
	_ = kcp.ListenWithOptions
	_ = grpc.NewServer
	_ credentials.TransportCredentials
	_ = insecure.NewCredentials
	_ = encoding.GetCodec
)

func acceptControl(ctx context.Context, opts ServerOpts, kind transportKind, onConn func(net.Conn), errCh chan<- error) {
	if kind.ws {
		err := serveWSControl(ctx, opts, kind, onConn)
		if err != nil && !errors.Is(err, context.Canceled) {
			select { case errCh <- err: default: }
		}
		return
	}
	if kind.grpc {
		err := serveGRPCControl(ctx, opts, kind, onConn)
		if err != nil && !errors.Is(err, context.Canceled) { select { case errCh <- err: default: } }
		return
	}
	if kind.http {
		err := serveHTTPConnectControl(ctx, opts, kind, onConn)
		if err != nil && !errors.Is(err, context.Canceled) { select { case errCh <- err: default: } }
		return
	}
	if kind.kcp {
		ln, err := listenKCPControl(opts.BindAddr)
		if err != nil { select { case errCh <- err: default: }; return }
		defer ln.Close()
		opts.Logger.Printf("kcp control listener on %s", opts.BindAddr)
		go func() { <-ctx.Done(); _ = ln.Close() }()
		for {
			conn, err := ln.Accept()
			if err != nil { if ctx.Err() != nil { return }; continue }
			tuneKCP(conn)
			if kind.slip {
				conn, err = wrapSlipstreamServer(conn)
				if err != nil { _ = conn.Close(); continue }
			}
			go onConn(conn)
		}
	}
	if kind.udp {
		bindAddr := opts.BindAddr
		if kind.dns { bindAddr = withDefaultPort(bindAddr, "53") }
		ln, err := listenUDPControl(bindAddr)
		if err != nil { select { case errCh <- err: default: }; return }
		defer ln.Close()
		if kind.dns { opts.Logger.Printf("dns(udp) control listener on %s", bindAddr) } else { opts.Logger.Printf("udp control listener on %s", bindAddr) }
		go func() { <-ctx.Done(); _ = ln.Close() }()
		for {
			conn, err := ln.Accept()
			if err != nil { if ctx.Err() != nil { return }; continue }
			if kind.slip {
				conn, err = wrapSlipstreamServer(conn)
				if err != nil { _ = conn.Close(); continue }
			}
			go onConn(conn)
		}
	}
	ln, err := listenTCPControl(opts.BindAddr, kind, opts.TLSCert, opts.TLSKey)
	if err != nil { select { case errCh <- err: default: }; return }
	defer ln.Close()
	opts.Logger.Printf("control listener on %s", opts.BindAddr)
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil { if ctx.Err() != nil { return }; continue }
		applyTCPOptions(conn, opts.NoDelay, opts.Keepalive)
		if kind.slip {
			conn, err = wrapSlipstreamServer(conn)
			if err != nil { _ = conn.Close(); continue }
		}
		go onConn(conn)
	}
}

func serveWSControl(ctx context.Context, opts ServerOpts, kind transportKind, onConn func(net.Conn)) error {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !headerContainsToken(r.Header.Get("Connection"), "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired); return
		}
		hj, ok := w.(http.Hijacker)
		if !ok { http.Error(w, "hijack not supported", http.StatusInternalServerError); return }
		conn, rw, err := hj.Hijack(); if err != nil { return }
		wsKey := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
		if wsKey == "" { _ = conn.Close(); return }
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
		_, _ = rw.WriteString("Upgrade: websocket\r\n")
		_, _ = rw.WriteString("Connection: Upgrade\r\n")
		_, _ = rw.WriteString("Sec-WebSocket-Accept: " + websocketAccept(wsKey) + "\r\n\r\n")
		_ = rw.Flush()
		base := prefixedConn{Conn: conn, r: rw.Reader}
		go onConn(newWSFrameConn(base, false))
	})
	srv := &http.Server{Addr: opts.BindAddr, Handler: h}
	go func() { <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()
	opts.Logger.Printf("ws control listener on %s", opts.BindAddr)
	if kind.tls {
		if opts.TLSCert == "" || opts.TLSKey == "" { return fmt.Errorf("wss/wssmux requires tls_cert and tls_key") }
		err := srv.ListenAndServeTLS(opts.TLSCert, opts.TLSKey)
		if err != nil && !errors.Is(err, http.ErrServerClosed) { return err }
		return nil
	}
	err := srv.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) { return err }
	return nil
}

func RunClient(ctx context.Context, opts ClientOpts) error {
	if opts.Logger == nil { opts.Logger = log.Default() }
	if usesOriginalTCPProtocol(opts.Transport) { return runOriginalTCPClient(ctx, opts) }
	kind, err := parseTransport(opts.Transport); if err != nil { return err }
	if opts.PoolSize <= 0 { opts.PoolSize = 8 }
	if opts.DialTimeout <= 0 { opts.DialTimeout = 10 * time.Second }
	if opts.RetryInterval <= 0 { opts.RetryInterval = 3 * time.Second }
	if opts.HeartbeatTimeout <= 0 { opts.HeartbeatTimeout = 25 * time.Second }
	if kind.mux { return runMuxClient(ctx, opts, kind) }
	return runWorkerClient(ctx, opts, kind)
}

func runWorkerClient(ctx context.Context, opts ClientOpts, kind transportKind) error {
	var wg sync.WaitGroup
	for i := 0; i < opts.PoolSize; i++ { wg.Add(1); go func(id int) { defer wg.Done(); runWorkerLoop(ctx, opts, kind, id) }(i) }
	<-ctx.Done(); wg.Wait(); return nil
}

func runWorkerLoop(ctx context.Context, opts ClientOpts, kind transportKind, id int) {
	_ = id
	dialer := net.Dialer{Timeout: opts.DialTimeout}
	for {
		select { case <-ctx.Done(): return; default: }
		conn, err := dialControlConn(ctx, opts, kind, roleWorker)
		if err != nil { time.Sleep(opts.RetryInterval); continue }
		_ = conn.SetReadDeadline(time.Now().Add(opts.HeartbeatTimeout))
		br := bufio.NewReader(conn)
		target, err := br.ReadString('\n')
		if err != nil { _ = conn.Close(); time.Sleep(opts.RetryInterval); continue }
		target = strings.TrimSpace(target)
		if strings.HasPrefix(target, "UDP ") {
			if err := handleUDPClientRelay(prefixedConn{Conn: conn, r: br}, strings.TrimSpace(strings.TrimPrefix(target, "UDP "))); err != nil { _ = conn.Close(); time.Sleep(opts.RetryInterval); continue }
			_ = conn.Close(); continue
		}
		up, err := dialer.DialContext(ctx, "tcp", target)
		if err != nil { _ = conn.Close(); time.Sleep(opts.RetryInterval); continue }
		applyTCPOptions(up, opts.NoDelay, opts.Keepalive)
		bridge(prefixedConn{Conn: conn, r: br}, up)
		_ = conn.Close(); _ = up.Close()
	}
}
