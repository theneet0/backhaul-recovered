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

const (
	roleWorker = "WORKER"
	roleMux    = "MUX"

	frameOpen  = 1
	frameData  = 2
	frameClose = 3

	slipMagic = "SLP1"
)

type ServerOpts struct {
	BindAddr          string
	Transport         string
	Token             string
	Mappings          []PortMapping
	AcceptUDP         bool
	NoDelay           bool
	Keepalive         time.Duration
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
	TLSCert           string
	TLSKey            string
	Logger            *log.Logger
}

type ClientOpts struct {
	RemoteAddr        string
	Transport         string
	Token             string
	DialTimeout       time.Duration
	RetryInterval     time.Duration
	PoolSize          int
	NoDelay           bool
	Keepalive         time.Duration
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
	TLSServerName     string
	Logger            *log.Logger
}

type transportKind struct {
	ws   bool
	http bool
	grpc bool
	kcp  bool
	tls  bool
	mux  bool
	slip bool
	udp  bool
	dns  bool
}

func RunServer(ctx context.Context, opts ServerOpts) error {
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	if usesOriginalTCPProtocol(opts.Transport) {
		return runOriginalTCPServer(ctx, opts)
	}
	kind, err := parseTransport(opts.Transport)
	if err != nil {
		return err
	}
	if opts.HeartbeatTimeout <= 0 {
		opts.HeartbeatTimeout = 25 * time.Second
	}

	if kind.mux {
		return runMuxServer(ctx, opts, kind)
	}
	return runWorkerServer(ctx, opts, kind)
}

func runWorkerServer(ctx context.Context, opts ServerOpts, kind transportKind) error {
	workerPool := make(chan net.Conn, 512)
	errCh := make(chan error, 1)

	handler := func(conn net.Conn) {
		authConn, role, err := authenticateControlConn(conn, opts.Token, opts.HeartbeatTimeout)
		if err != nil {
			_ = conn.Close()
			return
		}
		if role != roleWorker {
			_ = authConn.Close()
			return
		}
		select {
		case workerPool <- authConn:
		case <-ctx.Done():
			_ = authConn.Close()
		}
	}

	go acceptControl(ctx, opts, kind, handler, errCh)

	for _, m := range opts.Mappings {
		if err := startPublicListener(ctx, m, opts.Logger, func(reqCtx context.Context, target string) (net.Conn, error) {
			for {
				var worker net.Conn
				select {
				case worker = <-workerPool:
				case <-reqCtx.Done():
					return nil, reqCtx.Err()
				}
				if _, err := io.WriteString(worker, target+"\n"); err != nil {
					_ = worker.Close()
					continue
				}
				return worker, nil
			}
		}); err != nil {
			return err
		}
		if opts.AcceptUDP {
			if err := startPublicUDPListener(ctx, m, opts.Logger, func(reqCtx context.Context, target string) (net.Conn, error) {
				for {
					var worker net.Conn
					select {
					case worker = <-workerPool:
					case <-reqCtx.Done():
						return nil, reqCtx.Err()
					}
					if _, err := io.WriteString(worker, "UDP "+target+"\n"); err != nil {
						_ = worker.Close()
						continue
					}
					return worker, nil
				}
			}); err != nil {
				return err
			}
		}
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func runMuxServer(ctx context.Context, opts ServerOpts, kind transportKind) error {
	holder := &simpleMuxHolder{}
	errCh := make(chan error, 1)

	handler := func(conn net.Conn) {
		authConn, role, err := authenticateControlConn(conn, opts.Token, opts.HeartbeatTimeout)
		if err != nil {
			_ = conn.Close()
			return
		}
		if role != roleMux {
			_ = authConn.Close()
			return
		}
		sess := newSimpleMuxSession(authConn)
		holder.Add(sess)
		go func() {
			<-ctx.Done()
			_ = sess.Close()
		}()
		go func() {
			<-sess.Done()
			holder.Remove(sess)
		}()
	}

	go acceptControl(ctx, opts, kind, handler, errCh)

	for _, m := range opts.Mappings {
		if err := startPublicListener(ctx, m, opts.Logger, func(reqCtx context.Context, target string) (net.Conn, error) {
			stream, err := holder.OpenStream(reqCtx)
			if err != nil {
				return nil, err
			}
			if _, err := io.WriteString(stream, target+"\n"); err != nil {
				_ = stream.Close()
				return nil, err
			}
			return stream, nil
		}); err != nil {
			return err
		}
		if opts.AcceptUDP {
			if err := startPublicUDPListener(ctx, m, opts.Logger, func(reqCtx context.Context, target string) (net.Conn, error) {
				stream, err := holder.OpenStream(reqCtx)
				if err != nil {
					return nil, err
				}
				if _, err := io.WriteString(stream, "UDP "+target+"\n"); err != nil {
					_ = stream.Close()
					return nil, err
				}
				return stream, nil
			}); err != nil {
				return err
			}
		}
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func acceptControl(ctx context.Context, opts ServerOpts, kind transportKind, onConn func(net.Conn), errCh chan<- error) {
	if kind.ws {
		err := serveWSControl(ctx, opts, kind, onConn)
		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case errCh <- err:
			default:
			}
		}
		return
	}
	if kind.grpc {
		err := serveGRPCControl(ctx, opts, kind, onConn)
		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case errCh <- err:
			default:
			}
		}
		return
	}
	if kind.http {
		err := serveHTTPConnectControl(ctx, opts, kind, onConn)
		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case errCh <- err:
			default:
			}
		}
		return
	}
	if kind.kcp {
		ln, err := listenKCPControl(opts.BindAddr)
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
			return
		}
		defer ln.Close()
		opts.Logger.Printf("kcp control listener on %s", opts.BindAddr)
		go func() {
			<-ctx.Done()
			_ = ln.Close()
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			tuneKCP(conn)
			if kind.slip {
				conn, err = wrapSlipstreamServer(conn)
				if err != nil {
					_ = conn.Close()
					continue
				}
			}
			go onConn(conn)
		}
	}
	if kind.udp {
		bindAddr := opts.BindAddr
		if kind.dns {
			bindAddr = withDefaultPort(bindAddr, "53")
		}
		ln, err := listenUDPControl(bindAddr)
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
			return
		}
		defer ln.Close()
		if kind.dns {
			opts.Logger.Printf("dns(udp) control listener on %s", bindAddr)
		} else {
			opts.Logger.Printf("udp control listener on %s", bindAddr)
		}
		go func() {
			<-ctx.Done()
			_ = ln.Close()
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if kind.slip {
				conn, err = wrapSlipstreamServer(conn)
				if err != nil {
					_ = conn.Close()
					continue
				}
			}
			go onConn(conn)
		}
	}
	ln, err := listenTCPControl(opts.BindAddr, kind, opts.TLSCert, opts.TLSKey)
	if err != nil {
		select {
		case errCh <- err:
		default:
		}
		return
	}
	defer ln.Close()
	opts.Logger.Printf("control listener on %s", opts.BindAddr)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		applyTCPOptions(conn, opts.NoDelay, opts.Keepalive)
		if kind.slip {
			conn, err = wrapSlipstreamServer(conn)
			if err != nil {
				_ = conn.Close()
				continue
			}
		}
		go onConn(conn)
	}
}

func serveWSControl(ctx context.Context, opts ServerOpts, kind transportKind, onConn func(net.Conn)) error {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !headerContainsToken(r.Header.Get("Connection"), "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack not supported", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		wsKey := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
		if wsKey == "" {
			_ = conn.Close()
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
		_, _ = rw.WriteString("Upgrade: websocket\r\n")
		_, _ = rw.WriteString("Connection: Upgrade\r\n")
		_, _ = rw.WriteString("Sec-WebSocket-Accept: " + websocketAccept(wsKey) + "\r\n\r\n")
		_ = rw.Flush()
		base := prefixedConn{Conn: conn, r: rw.Reader}
		go onConn(newWSFrameConn(base, false))
	})
	srv := &http.Server{Addr: opts.BindAddr, Handler: h}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	opts.Logger.Printf("ws control listener on %s", opts.BindAddr)
	if kind.tls {
		if opts.TLSCert == "" || opts.TLSKey == "" {
			return fmt.Errorf("wss/wssmux requires tls_cert and tls_key")
		}
		err := srv.ListenAndServeTLS(opts.TLSCert, opts.TLSKey)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	err := srv.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func RunClient(ctx context.Context, opts ClientOpts) error {
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	if usesOriginalTCPProtocol(opts.Transport) {
		return runOriginalTCPClient(ctx, opts)
	}
	kind, err := parseTransport(opts.Transport)
	if err != nil {
		return err
	}
	if opts.PoolSize <= 0 {
		opts.PoolSize = 8
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = 3 * time.Second
	}
	if opts.HeartbeatTimeout <= 0 {
		opts.HeartbeatTimeout = 25 * time.Second
	}

	if kind.mux {
		return runMuxClient(ctx, opts, kind)
	}
	return runWorkerClient(ctx, opts, kind)
}

func runWorkerClient(ctx context.Context, opts ClientOpts, kind transportKind) error {
	var wg sync.WaitGroup
	for i := 0; i < opts.PoolSize; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runWorkerLoop(ctx, opts, kind, id)
		}(i)
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

func runWorkerLoop(ctx context.Context, opts ClientOpts, kind transportKind, id int) {
	_ = id
	dialer := net.Dialer{Timeout: opts.DialTimeout}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn, err := dialControlConn(ctx, opts, kind, roleWorker)
		if err != nil {
			time.Sleep(opts.RetryInterval)
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(opts.HeartbeatTimeout))
		br := bufio.NewReader(conn)
		target, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			time.Sleep(opts.RetryInterval)
			continue
		}
		target = strings.TrimSpace(target)
		if strings.HasPrefix(target, "UDP ") {
			if err := handleUDPClientRelay(prefixedConn{Conn: conn, r: br}, strings.TrimSpace(strings.TrimPrefix(target, "UDP "))); err != nil {
				_ = conn.Close()
				time.Sleep(opts.RetryInterval)
				continue
			}
			_ = conn.Close()
			continue
		}
		up, err := dialer.DialContext(ctx, "tcp", target)
		if err != nil {
			_ = conn.Close()
			time.Sleep(opts.RetryInterval)
			continue
		}
		applyTCPOptions(up, opts.NoDelay, opts.Keepalive)
		bridge(prefixedConn{Conn: conn, r: br}, up)
		_ = conn.Close()
		_ = up.Close()
	}
}

func runMuxClient(ctx context.Context, opts ClientOpts, kind transportKind) error {
	var wg sync.WaitGroup
	for i := 0; i < opts.PoolSize; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runMuxSessionLoop(ctx, opts, kind, id)
		}(i)
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

func runMuxSessionLoop(ctx context.Context, opts ClientOpts, kind transportKind, id int) {
	_ = id
	dialer := net.Dialer{Timeout: opts.DialTimeout}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn, err := dialControlConn(ctx, opts, kind, roleMux)
		if err != nil {
			time.Sleep(opts.RetryInterval)
			continue
		}
		sess := newSimpleMuxSession(conn)
		err = serveMuxClientSession(ctx, sess, dialer, opts)
		_ = sess.Close()
		_ = conn.Close()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			time.Sleep(opts.RetryInterval)
		}
	}
}

func serveMuxClientSession(ctx context.Context, sess *simpleMuxSession, dialer net.Dialer, opts ClientOpts) error {
	go func() {
		<-ctx.Done()
		_ = sess.Close()
	}()
	for {
		stream, err := sess.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func(s net.Conn) {
			br := bufio.NewReader(s)
			target, err := br.ReadString('\n')
			if err != nil {
				_ = s.Close()
				return
			}
			target = strings.TrimSpace(target)
			if strings.HasPrefix(target, "UDP ") {
				_ = handleUDPClientRelay(prefixedConn{Conn: s, r: br}, strings.TrimSpace(strings.TrimPrefix(target, "UDP ")))
				_ = s.Close()
				return
			}
			up, err := dialer.DialContext(ctx, "tcp", target)
			if err != nil {
				_ = s.Close()
				return
			}
			applyTCPOptions(up, opts.NoDelay, opts.Keepalive)
			bridge(prefixedConn{Conn: s, r: br}, up)
			_ = up.Close()
			_ = s.Close()
		}(stream)
	}
}

func dialControlConn(ctx context.Context, opts ClientOpts, kind transportKind, role string) (net.Conn, error) {
	var conn net.Conn
	var err error
	if kind.ws {
		conn, err = dialWSControl(ctx, opts, kind)
	} else if kind.grpc {
		conn, err = dialGRPCControl(ctx, opts, kind)
	} else if kind.http {
		conn, err = dialHTTPConnectControl(ctx, opts, kind)
	} else if kind.kcp {
		conn, err = dialKCPControl(ctx, opts, kind)
	} else if kind.udp {
		conn, err = dialUDPControl(ctx, opts, kind)
	} else {
		conn, err = dialTCPControl(ctx, opts, kind)
	}
	if err != nil {
		return nil, err
	}
	if kind.slip {
		conn, err = wrapSlipstreamClient(conn)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	if _, err := io.WriteString(conn, opts.Token+"\n"+role+"\n"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func dialUDPControl(ctx context.Context, opts ClientOpts, kind transportKind) (net.Conn, error) {
	d := net.Dialer{Timeout: opts.DialTimeout}
	remote := opts.RemoteAddr
	if kind.dns {
		remote = withDefaultPort(remote, "53")
	}
	return d.DialContext(ctx, "udp", remote)
}

func dialTCPControl(ctx context.Context, opts ClientOpts, kind transportKind) (net.Conn, error) {
	d := net.Dialer{Timeout: opts.DialTimeout}
	if kind.tls {
		sni := opts.TLSServerName
		if sni == "" {
			sni = extractHost(opts.RemoteAddr)
		}
		cfg := &tls.Config{InsecureSkipVerify: true, ServerName: sni}
		conn, err := tls.DialWithDialer(&d, "tcp", opts.RemoteAddr, cfg)
		if err != nil {
			return nil, err
		}
		applyTCPOptions(conn, opts.NoDelay, opts.Keepalive)
		return conn, nil
	}
	conn, err := d.DialContext(ctx, "tcp", opts.RemoteAddr)
	if err != nil {
		return nil, err
	}
	applyTCPOptions(conn, opts.NoDelay, opts.Keepalive)
	return conn, nil
}

func dialWSControl(ctx context.Context, opts ClientOpts, kind transportKind) (net.Conn, error) {
	u, err := buildWebsocketURL(opts.RemoteAddr, kind.tls)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	hostPort := parsed.Host
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}

	d := net.Dialer{Timeout: opts.DialTimeout}
	var raw net.Conn
	if kind.tls {
		sni := opts.TLSServerName
		if sni == "" {
			sni = extractHost(hostPort)
		}
		raw, err = tls.DialWithDialer(&d, "tcp", hostPort, &tls.Config{InsecureSkipVerify: true, ServerName: sni})
	} else {
		raw, err = d.DialContext(ctx, "tcp", hostPort)
	}
	if err != nil {
		return nil, err
	}

	br := bufio.NewReader(raw)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		_ = raw.Close()
		return nil, err
	}
	wsKey := base64.StdEncoding.EncodeToString(nonce)
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + hostPort + "\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + wsKey + "\r\n\r\n"
	if _, err := io.WriteString(raw, req); err != nil {
		_ = raw.Close()
		return nil, err
	}
	status, err := br.ReadString('\n')
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	if !strings.Contains(status, "101") {
		_ = raw.Close()
		return nil, fmt.Errorf("websocket upgrade failed: %s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = raw.Close()
			return nil, err
		}
		if line == "\r\n" {
			break
		}
	}
	base := prefixedConn{Conn: raw, r: br}
	return newWSFrameConn(base, true), nil
}

func serveHTTPConnectControl(ctx context.Context, opts ServerOpts, kind transportKind, onConn func(net.Conn)) error {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack not supported", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		base := prefixedConn{Conn: conn, r: rw.Reader}
		go onConn(base)
	})
	srv := &http.Server{Addr: opts.BindAddr, Handler: h}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	opts.Logger.Printf("http-connect control listener on %s", opts.BindAddr)
	if kind.tls {
		if opts.TLSCert == "" || opts.TLSKey == "" {
			return fmt.Errorf("https/httpsmux requires tls_cert and tls_key")
		}
		err := srv.ListenAndServeTLS(opts.TLSCert, opts.TLSKey)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	err := srv.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func dialHTTPConnectControl(ctx context.Context, opts ClientOpts, kind transportKind) (net.Conn, error) {
	hostPort, err := resolveHTTPControlHostPort(opts.RemoteAddr, kind.tls)
	if err != nil {
		return nil, err
	}

	d := net.Dialer{Timeout: opts.DialTimeout}
	var raw net.Conn
	if kind.tls {
		sni := opts.TLSServerName
		if sni == "" {
			sni = extractHost(hostPort)
		}
		raw, err = tls.DialWithDialer(&d, "tcp", hostPort, &tls.Config{InsecureSkipVerify: true, ServerName: sni})
	} else {
		raw, err = d.DialContext(ctx, "tcp", hostPort)
	}
	if err != nil {
		return nil, err
	}

	br := bufio.NewReader(raw)
	req := "CONNECT " + hostPort + " HTTP/1.1\r\n" +
		"Host: " + hostPort + "\r\n" +
		"Proxy-Connection: keep-alive\r\n\r\n"
	if _, err := io.WriteString(raw, req); err != nil {
		_ = raw.Close()
		return nil, err
	}

	status, err := br.ReadString('\n')
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	if !strings.Contains(status, "200") {
		_ = raw.Close()
		return nil, fmt.Errorf("http connect failed: %s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = raw.Close()
			return nil, err
		}
		if line == "\r\n" {
			break
		}
	}
	return prefixedConn{Conn: raw, r: br}, nil
}

func listenKCPControl(addr string) (net.Listener, error) {
	return kcp.ListenWithOptions(addr, nil, 10, 3)
}

func dialKCPControl(ctx context.Context, opts ClientOpts, _ transportKind) (net.Conn, error) {
	type out struct {
		c   net.Conn
		err error
	}
	ch := make(chan out, 1)
	go func() {
		c, err := kcp.DialWithOptions(opts.RemoteAddr, nil, 10, 3)
		ch <- out{c: c, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		tuneKCP(r.c)
		return r.c, nil
	}
}

func tuneKCP(conn net.Conn) {
	s, ok := conn.(*kcp.UDPSession)
	if !ok {
		return
	}
	s.SetStreamMode(true)
	s.SetWriteDelay(false)
	s.SetNoDelay(1, 20, 2, 1)
	s.SetMtu(1350)
	s.SetACKNoDelay(true)
}

type grpcCodecJSON struct{}

func (grpcCodecJSON) Name() string { return "json" }
func (grpcCodecJSON) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
func (grpcCodecJSON) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

var grpcCodecOnce sync.Once

func ensureGRPCCodec() {
	grpcCodecOnce.Do(func() {
		encoding.RegisterCodec(grpcCodecJSON{})
	})
}

type grpcChunk struct {
	Data []byte `json:"data,omitempty"`
	EOF  bool   `json:"eof,omitempty"`
}

type grpcStream interface {
	Context() context.Context
	SendMsg(m any) error
	RecvMsg(m any) error
}

type grpcControlService interface {
	Handle(grpc.ServerStream) error
}

type grpcControlServer struct {
	onConn func(net.Conn)
}

func (s *grpcControlServer) Handle(stream grpc.ServerStream) error {
	conn := newGRPCNetConn(stream, nil)
	go s.onConn(conn)
	<-stream.Context().Done()
	_ = conn.Close()
	return nil
}

func grpcControlHandler(srv any, stream grpc.ServerStream) error {
	return srv.(grpcControlService).Handle(stream)
}

var grpcControlServiceDesc = grpc.ServiceDesc{
	ServiceName: "backhaul.Control",
	HandlerType: (*grpcControlService)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName:    "Connect",
		Handler:       grpcControlHandler,
		ServerStreams: true,
		ClientStreams: true,
	}},
}

func serveGRPCControl(ctx context.Context, opts ServerOpts, kind transportKind, onConn func(net.Conn)) error {
	ensureGRPCCodec()
	var creds credentials.TransportCredentials
	if kind.tls {
		if opts.TLSCert == "" || opts.TLSKey == "" {
			return fmt.Errorf("grpcs/grpcsmux requires tls_cert and tls_key")
		}
		cert, err := tls.LoadX509KeyPair(opts.TLSCert, opts.TLSKey)
		if err != nil {
			return err
		}
		creds = credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}})
	} else {
		creds = insecure.NewCredentials()
	}

	s := grpc.NewServer(grpc.Creds(creds))
	s.RegisterService(&grpcControlServiceDesc, &grpcControlServer{onConn: onConn})
	ln, err := net.Listen("tcp", opts.BindAddr)
	if err != nil {
		return err
	}
	defer ln.Close()
	opts.Logger.Printf("grpc control listener on %s", opts.BindAddr)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Serve(ln)
	}()
	select {
	case <-ctx.Done():
		s.GracefulStop()
		<-done
		return nil
	case <-done:
		return fmt.Errorf("grpc server stopped")
	}
}

func dialGRPCControl(ctx context.Context, opts ClientOpts, kind transportKind) (net.Conn, error) {
	ensureGRPCCodec()
	target, err := resolveGRPCTarget(opts.RemoteAddr, kind.tls)
	if err != nil {
		return nil, err
	}
	var creds credentials.TransportCredentials
	if kind.tls {
		sni := opts.TLSServerName
		if sni == "" {
			sni = extractHost(target)
		}
		creds = credentials.NewTLS(&tls.Config{InsecureSkipVerify: true, ServerName: sni})
	} else {
		creds = insecure.NewCredentials()
	}

	cc, err := grpc.DialContext(
		ctx,
		target,
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock(),
		grpc.WithDefaultCallOptions(grpc.CallContentSubtype("json")),
	)
	if err != nil {
		return nil, err
	}
	desc := &grpc.StreamDesc{StreamName: "Connect", ServerStreams: true, ClientStreams: true}
	cs, err := cc.NewStream(ctx, desc, "/backhaul.Control/Connect")
	if err != nil {
		_ = cc.Close()
		return nil, err
	}
	return newGRPCNetConn(cs, cc.Close), nil
}

type grpcNetConn struct {
	stream  grpcStream
	onClose func() error
	rmu     sync.Mutex
	wmu     sync.Mutex
	rbuf    bytes.Buffer
	closed  atomic.Bool
}

func newGRPCNetConn(stream grpcStream, onClose func() error) net.Conn {
	return &grpcNetConn{stream: stream, onClose: onClose}
}

func (c *grpcNetConn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for c.rbuf.Len() == 0 {
		var chunk grpcChunk
		if err := c.stream.RecvMsg(&chunk); err != nil {
			return 0, err
		}
		if chunk.EOF {
			return 0, io.EOF
		}
		_, _ = c.rbuf.Write(chunk.Data)
	}
	return c.rbuf.Read(p)
}

func (c *grpcNetConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	cp := make([]byte, len(p))
	copy(cp, p)
	if err := c.stream.SendMsg(&grpcChunk{Data: cp}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *grpcNetConn) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	_ = c.stream.SendMsg(&grpcChunk{EOF: true})
	if cs, ok := c.stream.(interface{ CloseSend() error }); ok {
		_ = cs.CloseSend()
	}
	if c.onClose != nil {
		return c.onClose()
	}
	return nil
}

func (c *grpcNetConn) LocalAddr() net.Addr  { return stringAddr{n: "grpc", a: "local"} }
func (c *grpcNetConn) RemoteAddr() net.Addr { return stringAddr{n: "grpc", a: "remote"} }
func (c *grpcNetConn) SetDeadline(_ time.Time) error {
	return nil
}
func (c *grpcNetConn) SetReadDeadline(_ time.Time) error {
	return nil
}
func (c *grpcNetConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

func authenticateControlConn(conn net.Conn, token string, readTimeout time.Duration) (net.Conn, string, error) {
	if readTimeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	}
	br := bufio.NewReader(conn)
	tok, err := br.ReadString('\n')
	if err != nil {
		return nil, "", err
	}
	role, err := br.ReadString('\n')
	if err != nil {
		return nil, "", err
	}
	_ = conn.SetReadDeadline(time.Time{})
	tok = strings.TrimSpace(tok)
	if token != "" && tok != token {
		return nil, "", fmt.Errorf("invalid token")
	}
	return prefixedConn{Conn: conn, r: br}, strings.TrimSpace(role), nil
}

func listenTCPControl(addr string, kind transportKind, certPath, keyPath string) (net.Listener, error) {
	if !kind.tls {
		return net.Listen("tcp", addr)
	}
	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("anytls requires tls_cert and tls_key")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	return tls.Listen("tcp", addr, cfg)
}

type udpControlListener struct {
	pc      *net.UDPConn
	acceptC chan net.Conn
	closed  chan struct{}
	closeW  sync.Once
	mu      sync.Mutex
	conns   map[string]*udpControlConn
}

func listenUDPControl(addr string) (net.Listener, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	pc, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	l := &udpControlListener{
		pc:      pc,
		acceptC: make(chan net.Conn, 256),
		closed:  make(chan struct{}),
		conns:   map[string]*udpControlConn{},
	}
	go l.readLoop()
	return l, nil
}

func (l *udpControlListener) readLoop() {
	buf := make([]byte, 64*1024)
	for {
		n, raddr, err := l.pc.ReadFromUDP(buf)
		if err != nil {
			return
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		key := raddr.String()

		l.mu.Lock()
		c := l.conns[key]
		if c == nil {
			c = &udpControlConn{
				parent:   l,
				remote:   raddr,
				key:      key,
				incoming: make(chan []byte, 512),
				closed:   make(chan struct{}),
			}
			l.conns[key] = c
			select {
			case l.acceptC <- c:
			default:
				delete(l.conns, key)
				close(c.closed)
				l.mu.Unlock()
				continue
			}
		}
		l.mu.Unlock()

		if !c.push(pkt) {
			_ = c.Close()
		}
	}
}

func (l *udpControlListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case c := <-l.acceptC:
		return c, nil
	}
}

func (l *udpControlListener) Close() error {
	var err error
	l.closeW.Do(func() {
		close(l.closed)
		err = l.pc.Close()
		l.mu.Lock()
		for _, c := range l.conns {
			_ = c.closeLocal()
		}
		l.conns = map[string]*udpControlConn{}
		l.mu.Unlock()
	})
	return err
}

func (l *udpControlListener) Addr() net.Addr { return l.pc.LocalAddr() }

func (l *udpControlListener) remove(key string) {
	l.mu.Lock()
	delete(l.conns, key)
	l.mu.Unlock()
}

type udpControlConn struct {
	parent   *udpControlListener
	remote   *net.UDPAddr
	key      string
	incoming chan []byte
	closed   chan struct{}
	closeW   sync.Once
	rmu      sync.Mutex
	rbuf     bytes.Buffer
	wmu      sync.Mutex
}

func (c *udpControlConn) push(pkt []byte) bool {
	select {
	case <-c.closed:
		return false
	case c.incoming <- pkt:
		return true
	default:
		return false
	}
}

func (c *udpControlConn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for c.rbuf.Len() == 0 {
		select {
		case <-c.closed:
			return 0, io.EOF
		case pkt := <-c.incoming:
			_, _ = c.rbuf.Write(pkt)
		}
	}
	return c.rbuf.Read(p)
}

func (c *udpControlConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	return c.parent.pc.WriteToUDP(p, c.remote)
}

func (c *udpControlConn) closeLocal() error {
	c.closeW.Do(func() {
		close(c.closed)
		c.parent.remove(c.key)
	})
	return nil
}

func (c *udpControlConn) Close() error { return c.closeLocal() }
func (c *udpControlConn) LocalAddr() net.Addr {
	return c.parent.pc.LocalAddr()
}
func (c *udpControlConn) RemoteAddr() net.Addr { return c.remote }
func (c *udpControlConn) SetDeadline(_ time.Time) error {
	return nil
}
func (c *udpControlConn) SetReadDeadline(_ time.Time) error {
	return nil
}
func (c *udpControlConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

func startPublicListener(ctx context.Context, m PortMapping, lg *log.Logger, acquire func(context.Context, string) (net.Conn, error)) error {
	ln, err := net.Listen("tcp", m.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", m.ListenAddr, err)
	}
	lg.Printf("public listener %s -> %s", m.ListenAddr, m.TargetAddr)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
				defer cancel()
				backend, err := acquire(reqCtx, m.TargetAddr)
				if err != nil {
					return
				}
				defer backend.Close()
				bridge(c, backend)
			}(in)
		}
	}()
	return nil
}

type udpAssociation struct {
	conn net.Conn
	addr *net.UDPAddr
	mu   sync.Mutex
}

func startPublicUDPListener(ctx context.Context, m PortMapping, lg *log.Logger, acquire func(context.Context, string) (net.Conn, error)) error {
	udpAddr, err := net.ResolveUDPAddr("udp", m.ListenAddr)
	if err != nil {
		return fmt.Errorf("resolve udp %s: %w", m.ListenAddr, err)
	}
	ln, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp %s: %w", m.ListenAddr, err)
	}
	lg.Printf("public udp listener %s -> %s", m.ListenAddr, m.TargetAddr)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var assocMu sync.Mutex
	assocs := map[string]*udpAssociation{}
	buf := make([]byte, 64*1024)
	for {
		n, src, err := ln.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("udp read error on %s: %w", m.ListenAddr, err)
		}
		key := src.String()
		assocMu.Lock()
		assoc := assocs[key]
		assocMu.Unlock()
		if assoc == nil {
			reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			c, err := acquire(reqCtx, m.TargetAddr)
			cancel()
			if err != nil {
				continue
			}
			assoc = &udpAssociation{conn: c, addr: src}
			assocMu.Lock()
			assocs[key] = assoc
			assocMu.Unlock()

			go func(k string, a *udpAssociation) {
				defer func() {
					_ = a.conn.Close()
					assocMu.Lock()
					delete(assocs, k)
					assocMu.Unlock()
				}()
				for {
					pkt, err := readDatagramFrame(a.conn)
					if err != nil {
						return
					}
					_, _ = ln.WriteToUDP(pkt, a.addr)
				}
			}(key, assoc)
		}
		assoc.mu.Lock()
		err = writeDatagramFrame(assoc.conn, buf[:n])
		assoc.mu.Unlock()
		if err != nil {
			_ = assoc.conn.Close()
		}
	}
}

func bridge(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		_ = a.SetReadDeadline(time.Now())
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		_ = b.SetReadDeadline(time.Now())
	}()
	wg.Wait()
}

func handleUDPClientRelay(control net.Conn, target string) error {
	up, err := net.Dial("udp", target)
	if err != nil {
		return err
	}
	defer up.Close()
	errCh := make(chan error, 2)
	go func() {
		for {
			pkt, err := readDatagramFrame(control)
			if err != nil {
				errCh <- err
				return
			}
			if _, err := up.Write(pkt); err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := up.Read(buf)
			if err != nil {
				errCh <- err
				return
			}
			if err := writeDatagramFrame(control, buf[:n]); err != nil {
				errCh <- err
				return
			}
		}
	}()
	return <-errCh
}

func writeDatagramFrame(w io.Writer, p []byte) error {
	if len(p) > 65535 {
		p = p[:65535]
	}
	hdr := []byte{0, 0}
	binary.BigEndian.PutUint16(hdr, uint16(len(p)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(p)
	return err
}

func readDatagramFrame(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr))
	if n == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

type prefixedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c prefixedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

// ValidateTransport reports whether raw is a transport name accepted by the
// recovered runtime. It is used by configuration tooling before startup.
func ValidateTransport(raw string) error {
	_, err := parseTransport(raw)
	return err
}

func parseTransport(raw string) (transportKind, error) {
	t := strings.ToLower(strings.TrimSpace(raw))
	switch t {
	case "", "tcp":
		return transportKind{}, nil
	case "kcp":
		return transportKind{kcp: true}, nil
	case "kcpmux", "xkcpmux":
		return transportKind{kcp: true, mux: true}, nil
	case "grpc":
		return transportKind{grpc: true}, nil
	case "grpcs":
		return transportKind{grpc: true, tls: true}, nil
	case "grpcmux", "xgrpcmux":
		return transportKind{grpc: true, mux: true}, nil
	case "grpcsmux", "xgrpcsmux":
		return transportKind{grpc: true, tls: true, mux: true}, nil
	case "http":
		return transportKind{http: true}, nil
	case "https":
		return transportKind{http: true, tls: true}, nil
	case "httpmux", "xhttpmux":
		return transportKind{http: true, mux: true}, nil
	case "httpsmux", "xhttpsmux":
		return transportKind{http: true, tls: true, mux: true}, nil
	case "udp":
		return transportKind{udp: true}, nil
	case "udpmux", "xudpmux":
		return transportKind{udp: true, mux: true}, nil
	case "dns":
		return transportKind{udp: true, dns: true}, nil
	case "dnsmux", "xdnsmux":
		return transportKind{udp: true, dns: true, mux: true}, nil
	case "slipstream", "slip", "sstream":
		return transportKind{slip: true}, nil
	case "slipstreammux", "slipmux", "sstreammux":
		return transportKind{slip: true, mux: true}, nil
	case "raw", "rawsocket", "socketraw":
		// Raw-socket mode is mapped to stream baseline path for compatibility.
		return transportKind{}, nil
	case "tun":
		return transportKind{mux: true}, nil
	case "anytls":
		return transportKind{tls: true}, nil
	case "tcpmux", "xtcpmux":
		return transportKind{mux: true}, nil
	case "ws":
		return transportKind{ws: true}, nil
	case "wss":
		return transportKind{ws: true, tls: true}, nil
	case "wsmux", "xwsmux":
		return transportKind{ws: true, mux: true}, nil
	case "wssmux":
		return transportKind{ws: true, tls: true, mux: true}, nil
	default:
		return transportKind{}, fmt.Errorf("unsupported transport type: %s", raw)
	}
}

func withDefaultPort(addr, defaultPort string) string {
	if addr == "" {
		return ":" + defaultPort
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	if strings.Count(addr, ":") > 1 && !strings.HasPrefix(addr, "[") {
		return "[" + addr + "]:" + defaultPort
	}
	return addr + ":" + defaultPort
}

func resolveHTTPControlHostPort(remote string, secure bool) (string, error) {
	if strings.HasPrefix(remote, "http://") || strings.HasPrefix(remote, "https://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", err
		}
		if u.Host == "" {
			return "", fmt.Errorf("invalid remote_addr for http: %s", remote)
		}
		if _, _, err := net.SplitHostPort(u.Host); err != nil {
			if secure {
				return net.JoinHostPort(u.Host, "443"), nil
			}
			return net.JoinHostPort(u.Host, "80"), nil
		}
		return u.Host, nil
	}
	if _, _, err := net.SplitHostPort(remote); err == nil {
		return remote, nil
	}
	if secure {
		return withDefaultPort(remote, "443"), nil
	}
	return withDefaultPort(remote, "80"), nil
}

func resolveGRPCTarget(remote string, secure bool) (string, error) {
	if strings.HasPrefix(remote, "grpc://") || strings.HasPrefix(remote, "grpcs://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", err
		}
		if u.Host == "" {
			return "", fmt.Errorf("invalid remote_addr for grpc: %s", remote)
		}
		if _, _, err := net.SplitHostPort(u.Host); err == nil {
			return u.Host, nil
		}
		if secure {
			return net.JoinHostPort(u.Host, "443"), nil
		}
		return net.JoinHostPort(u.Host, "80"), nil
	}
	if _, _, err := net.SplitHostPort(remote); err == nil {
		return remote, nil
	}
	if secure {
		return withDefaultPort(remote, "443"), nil
	}
	return withDefaultPort(remote, "80"), nil
}

type stringAddr struct {
	n string
	a string
}

func (a stringAddr) Network() string { return a.n }
func (a stringAddr) String() string  { return a.a }

func applyTCPOptions(conn net.Conn, noDelay bool, keepalive time.Duration) {
	tc, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tc.SetNoDelay(noDelay)
	if keepalive > 0 {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(keepalive)
	}
}

func buildWebsocketURL(addr string, secure bool) (string, error) {
	if strings.HasPrefix(addr, "ws://") || strings.HasPrefix(addr, "wss://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", err
		}
		if u.Path == "" {
			u.Path = "/"
		}
		return u.String(), nil
	}
	scheme := "ws"
	if secure {
		scheme = "wss"
	}
	u, err := url.Parse(scheme + "://" + addr)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid remote_addr for websocket: %s", addr)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

func extractHost(addr string) string {
	h := addr
	if strings.Contains(addr, "://") {
		if u, err := url.Parse(addr); err == nil {
			h = u.Host
		}
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

func headerContainsToken(v, token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	for _, p := range strings.Split(v, ",") {
		if strings.ToLower(strings.TrimSpace(p)) == token {
			return true
		}
	}
	return false
}

func websocketAccept(k string) string {
	const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	sum := sha1.Sum([]byte(k + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

type wsFrameConn struct {
	conn     net.Conn
	isClient bool
	wmu      sync.Mutex
	rmu      sync.Mutex
	rbuf     bytes.Buffer
}

func newWSFrameConn(c net.Conn, isClient bool) net.Conn {
	return &wsFrameConn{conn: c, isClient: isClient}
}

func (w *wsFrameConn) Read(p []byte) (int, error) {
	w.rmu.Lock()
	defer w.rmu.Unlock()
	for w.rbuf.Len() == 0 {
		if err := w.readNextDataFrame(); err != nil {
			return 0, err
		}
	}
	return w.rbuf.Read(p)
}

func (w *wsFrameConn) Write(p []byte) (int, error) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	const maxPayload = 65535
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxPayload {
			chunk = p[:maxPayload]
		}
		if err := w.writeFrame(0x2, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (w *wsFrameConn) Close() error {
	_ = w.writeFrame(0x8, nil)
	return w.conn.Close()
}

func (w *wsFrameConn) LocalAddr() net.Addr                { return w.conn.LocalAddr() }
func (w *wsFrameConn) RemoteAddr() net.Addr               { return w.conn.RemoteAddr() }
func (w *wsFrameConn) SetDeadline(t time.Time) error      { return w.conn.SetDeadline(t) }
func (w *wsFrameConn) SetReadDeadline(t time.Time) error  { return w.conn.SetReadDeadline(t) }
func (w *wsFrameConn) SetWriteDeadline(t time.Time) error { return w.conn.SetWriteDeadline(t) }

func (w *wsFrameConn) readNextDataFrame() error {
	var h [2]byte
	if _, err := io.ReadFull(w.conn, h[:]); err != nil {
		return err
	}
	opcode := h[0] & 0x0F
	hasMask := (h[1] & 0x80) != 0
	n, err := wsReadLen(w.conn, h[1]&0x7F)
	if err != nil {
		return err
	}
	var maskKey [4]byte
	if hasMask {
		if _, err := io.ReadFull(w.conn, maskKey[:]); err != nil {
			return err
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(w.conn, payload); err != nil {
		return err
	}
	if hasMask {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	switch opcode {
	case 0x1, 0x2, 0x0:
		_, _ = w.rbuf.Write(payload)
		return nil
	case 0x8:
		return io.EOF
	case 0x9:
		_ = w.writeFrame(0xA, payload)
		return nil
	case 0xA:
		return nil
	default:
		return nil
	}
}

func (w *wsFrameConn) writeFrame(opcode byte, payload []byte) error {
	const fin = 0x80
	maskBit := byte(0)
	if w.isClient {
		maskBit = 0x80
	}

	hdr := []byte{fin | (opcode & 0x0F)}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, maskBit|byte(n))
	case n <= 65535:
		hdr = append(hdr, maskBit|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, maskBit|127,
			byte(uint64(n)>>56), byte(uint64(n)>>48), byte(uint64(n)>>40), byte(uint64(n)>>32),
			byte(uint64(n)>>24), byte(uint64(n)>>16), byte(uint64(n)>>8), byte(uint64(n)))
	}
	if _, err := w.conn.Write(hdr); err != nil {
		return err
	}
	if w.isClient {
		var mk [4]byte
		if _, err := rand.Read(mk[:]); err != nil {
			return err
		}
		if _, err := w.conn.Write(mk[:]); err != nil {
			return err
		}
		masked := make([]byte, len(payload))
		copy(masked, payload)
		for i := range masked {
			masked[i] ^= mk[i%4]
		}
		_, err := w.conn.Write(masked)
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.conn.Write(payload)
	return err
}

func wsReadLen(r io.Reader, n0 byte) (int, error) {
	switch n0 {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, err
		}
		return int(binary.BigEndian.Uint16(b[:])), nil
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, err
		}
		n := binary.BigEndian.Uint64(b[:])
		if n > (1<<31 - 1) {
			return 0, fmt.Errorf("websocket frame too large: %d", n)
		}
		return int(n), nil
	default:
		return int(n0), nil
	}
}

func wrapSlipstreamServer(conn net.Conn) (net.Conn, error) {
	var hello [4 + 16]byte
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	if _, err := io.ReadFull(conn, hello[:]); err != nil {
		return conn, err
	}
	if string(hello[:4]) != slipMagic {
		return conn, fmt.Errorf("invalid slipstream hello")
	}
	var serverNonce [16]byte
	if _, err := rand.Read(serverNonce[:]); err != nil {
		return conn, err
	}
	reply := make([]byte, 4+16)
	copy(reply[:4], []byte(slipMagic))
	copy(reply[4:], serverNonce[:])
	if _, err := conn.Write(reply); err != nil {
		return conn, err
	}
	_ = conn.SetReadDeadline(time.Time{})

	key := sha256.Sum256(append(hello[4:20], serverNonce[:]...))
	return newSlipstreamConn(conn, key), nil
}

func wrapSlipstreamClient(conn net.Conn) (net.Conn, error) {
	var clientNonce [16]byte
	if _, err := rand.Read(clientNonce[:]); err != nil {
		return conn, err
	}
	hello := make([]byte, 4+16)
	copy(hello[:4], []byte(slipMagic))
	copy(hello[4:], clientNonce[:])
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err := conn.Write(hello); err != nil {
		return conn, err
	}

	var reply [4 + 16]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return conn, err
	}
	if string(reply[:4]) != slipMagic {
		return conn, fmt.Errorf("invalid slipstream reply")
	}
	_ = conn.SetDeadline(time.Time{})
	key := sha256.Sum256(append(clientNonce[:], reply[4:20]...))
	return newSlipstreamConn(conn, key), nil
}

type slipstreamConn struct {
	conn net.Conn
	key  [32]byte
	rmu  sync.Mutex
	wmu  sync.Mutex
	rbuf bytes.Buffer
	roff uint64
	woff uint64
}

func newSlipstreamConn(conn net.Conn, key [32]byte) net.Conn {
	return &slipstreamConn{conn: conn, key: key}
}

func (s *slipstreamConn) Read(p []byte) (int, error) {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	for s.rbuf.Len() == 0 {
		var hdr [2]byte
		if _, err := io.ReadFull(s.conn, hdr[:]); err != nil {
			return 0, err
		}
		n := int(binary.BigEndian.Uint16(hdr[:]))
		if n == 0 {
			continue
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(s.conn, payload); err != nil {
			return 0, err
		}
		s.xor(payload, s.roff)
		s.roff += uint64(len(payload))
		_, _ = s.rbuf.Write(payload)
	}
	return s.rbuf.Read(p)
}

func (s *slipstreamConn) Write(p []byte) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > 65535 {
			chunk = p[:65535]
		}
		enc := make([]byte, len(chunk))
		copy(enc, chunk)
		s.xor(enc, s.woff)
		s.woff += uint64(len(enc))

		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], uint16(len(enc)))
		if _, err := s.conn.Write(hdr[:]); err != nil {
			return written, err
		}
		if _, err := s.conn.Write(enc); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (s *slipstreamConn) xor(b []byte, offset uint64) {
	for i := range b {
		b[i] ^= s.key[(offset+uint64(i))%uint64(len(s.key))]
	}
}

func (s *slipstreamConn) Close() error                      { return s.conn.Close() }
func (s *slipstreamConn) LocalAddr() net.Addr               { return s.conn.LocalAddr() }
func (s *slipstreamConn) RemoteAddr() net.Addr              { return s.conn.RemoteAddr() }
func (s *slipstreamConn) SetDeadline(t time.Time) error     { return s.conn.SetDeadline(t) }
func (s *slipstreamConn) SetReadDeadline(t time.Time) error { return s.conn.SetReadDeadline(t) }
func (s *slipstreamConn) SetWriteDeadline(t time.Time) error {
	return s.conn.SetWriteDeadline(t)
}

type simpleMuxHolder struct {
	mu       sync.Mutex
	sessions []*simpleMuxSession
	next     int
}

func (h *simpleMuxHolder) Add(s *simpleMuxSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions = append(h.sessions, s)
}

func (h *simpleMuxHolder) Remove(s *simpleMuxSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.sessions[:0]
	for _, it := range h.sessions {
		if it != s && !it.IsClosed() {
			out = append(out, it)
		}
	}
	h.sessions = out
	if h.next >= len(h.sessions) {
		h.next = 0
	}
}

func (h *simpleMuxHolder) OpenStream(ctx context.Context) (net.Conn, error) {
	for {
		s := h.nextSession()
		if s != nil {
			stream, err := s.OpenStream()
			if err == nil {
				return stream, nil
			}
			h.Remove(s)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(120 * time.Millisecond):
		}
	}
}

func (h *simpleMuxHolder) nextSession() *simpleMuxSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sessions) == 0 {
		return nil
	}
	for i := 0; i < len(h.sessions); i++ {
		idx := (h.next + i) % len(h.sessions)
		s := h.sessions[idx]
		if s == nil || s.IsClosed() {
			continue
		}
		h.next = (idx + 1) % len(h.sessions)
		return s
	}
	return nil
}

type simpleMuxSession struct {
	conn      net.Conn
	writeMu   sync.Mutex
	streamsMu sync.Mutex
	streams   map[uint32]*simpleMuxStream
	acceptCh  chan *simpleMuxStream
	nextID    atomic.Uint32
	closed    atomic.Bool
	done      chan struct{}
}

func newSimpleMuxSession(conn net.Conn) *simpleMuxSession {
	s := &simpleMuxSession{
		conn:     conn,
		streams:  make(map[uint32]*simpleMuxStream),
		acceptCh: make(chan *simpleMuxStream, 1024),
		done:     make(chan struct{}),
	}
	s.nextID.Store(1)
	go s.readLoop()
	return s
}

func (s *simpleMuxSession) Done() <-chan struct{} { return s.done }

func (s *simpleMuxSession) IsClosed() bool { return s.closed.Load() }

func (s *simpleMuxSession) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	close(s.done)
	_ = s.conn.Close()
	s.streamsMu.Lock()
	for _, st := range s.streams {
		st.remoteClose()
	}
	s.streams = map[uint32]*simpleMuxStream{}
	s.streamsMu.Unlock()
	return nil
}

func (s *simpleMuxSession) Accept(ctx context.Context) (net.Conn, error) {
	select {
	case st := <-s.acceptCh:
		return st, nil
	case <-s.done:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *simpleMuxSession) OpenStream() (net.Conn, error) {
	if s.IsClosed() {
		return nil, io.EOF
	}
	id := s.nextID.Add(1)
	st := newSimpleMuxStream(s, id)
	s.streamsMu.Lock()
	s.streams[id] = st
	s.streamsMu.Unlock()
	if err := s.writeFrame(frameOpen, id, nil); err != nil {
		s.removeStream(id)
		return nil, err
	}
	return st, nil
}

func (s *simpleMuxSession) readLoop() {
	hdr := make([]byte, 9)
	for {
		if _, err := io.ReadFull(s.conn, hdr); err != nil {
			_ = s.Close()
			return
		}
		ft := hdr[0]
		id := binary.BigEndian.Uint32(hdr[1:5])
		n := binary.BigEndian.Uint32(hdr[5:9])
		var payload []byte
		if n > 0 {
			payload = make([]byte, n)
			if _, err := io.ReadFull(s.conn, payload); err != nil {
				_ = s.Close()
				return
			}
		}
		s.handleFrame(ft, id, payload)
	}
}

func (s *simpleMuxSession) handleFrame(ft byte, id uint32, payload []byte) {
	switch ft {
	case frameOpen:
		st := newSimpleMuxStream(s, id)
		s.streamsMu.Lock()
		s.streams[id] = st
		s.streamsMu.Unlock()
		select {
		case s.acceptCh <- st:
		default:
			_ = st.Close()
		}
	case frameData:
		st := s.getStream(id)
		if st == nil {
			return
		}
		st.pushData(payload)
	case frameClose:
		st := s.getStream(id)
		if st == nil {
			return
		}
		st.remoteClose()
		s.removeStream(id)
	}
}

func (s *simpleMuxSession) writeFrame(ft byte, id uint32, payload []byte) error {
	if s.IsClosed() {
		return io.EOF
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	hdr := make([]byte, 9)
	hdr[0] = ft
	binary.BigEndian.PutUint32(hdr[1:5], id)
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(payload)))
	if _, err := s.conn.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := s.conn.Write(payload)
		return err
	}
	return nil
}

func (s *simpleMuxSession) getStream(id uint32) *simpleMuxStream {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	return s.streams[id]
}

func (s *simpleMuxSession) removeStream(id uint32) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	delete(s.streams, id)
}

type simpleMuxStream struct {
	sess         *simpleMuxSession
	id           uint32
	readCh       chan []byte
	readBuf      bytes.Buffer
	readMu       sync.Mutex
	readCond     *sync.Cond
	readClosed   bool
	closeOnce    sync.Once
	localClosed  atomic.Bool
	deadlineRead atomic.Value
	deadlineWri  atomic.Value
}

func newSimpleMuxStream(sess *simpleMuxSession, id uint32) *simpleMuxStream {
	st := &simpleMuxStream{sess: sess, id: id, readCh: make(chan []byte, 256)}
	st.readCond = sync.NewCond(&st.readMu)
	go st.ingestLoop()
	return st
}

func (s *simpleMuxStream) ingestLoop() {
	for p := range s.readCh {
		s.readMu.Lock()
		_, _ = s.readBuf.Write(p)
		s.readCond.Broadcast()
		s.readMu.Unlock()
	}
	s.readMu.Lock()
	s.readClosed = true
	s.readCond.Broadcast()
	s.readMu.Unlock()
}

func (s *simpleMuxStream) pushData(p []byte) {
	if len(p) == 0 {
		return
	}
	cp := make([]byte, len(p))
	copy(cp, p)
	select {
	case s.readCh <- cp:
	default:
		_ = s.Close()
	}
}

func (s *simpleMuxStream) remoteClose() {
	s.closeOnce.Do(func() { close(s.readCh) })
}

func (s *simpleMuxStream) Read(p []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for s.readBuf.Len() == 0 && !s.readClosed {
		s.readCond.Wait()
	}
	if s.readBuf.Len() == 0 && s.readClosed {
		return 0, io.EOF
	}
	return s.readBuf.Read(p)
}

func (s *simpleMuxStream) Write(p []byte) (int, error) {
	if s.localClosed.Load() || s.sess.IsClosed() {
		return 0, io.EOF
	}
	const maxChunk = 32 * 1024
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxChunk {
			chunk = p[:maxChunk]
		}
		if err := s.sess.writeFrame(frameData, s.id, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (s *simpleMuxStream) Close() error {
	if s.localClosed.Swap(true) {
		return nil
	}
	s.sess.removeStream(s.id)
	_ = s.sess.writeFrame(frameClose, s.id, nil)
	s.remoteClose()
	return nil
}

func (s *simpleMuxStream) LocalAddr() net.Addr  { return s.sess.conn.LocalAddr() }
func (s *simpleMuxStream) RemoteAddr() net.Addr { return s.sess.conn.RemoteAddr() }
func (s *simpleMuxStream) SetDeadline(t time.Time) error {
	s.deadlineRead.Store(t)
	s.deadlineWri.Store(t)
	return nil
}
func (s *simpleMuxStream) SetReadDeadline(t time.Time) error  { s.deadlineRead.Store(t); return nil }
func (s *simpleMuxStream) SetWriteDeadline(t time.Time) error { s.deadlineWri.Store(t); return nil }
