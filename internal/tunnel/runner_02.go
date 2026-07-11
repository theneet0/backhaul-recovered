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
	_ = bufio.NewReader; _ = bytes.NewBuffer; _ = context.Background; _ = rand.Reader; _ = sha1.New; _ = sha256.New
	_ = tls.VersionTLS13; _ = base64.StdEncoding; _ = binary.BigEndian; _ = json.Marshal; _ = errors.New; _ = fmt.Sprintf
	_ = io.EOF; _ = log.Default; _ = net.IPv4len; _ = http.MethodGet; _ = url.URL{}; _ = strings.TrimSpace
	_ = sync.Mutex{}; _ = atomic.Bool{}; _ = time.Second; _ = kcp.ListenWithOptions; _ = grpc.NewServer
	_ credentials.TransportCredentials; _ = insecure.NewCredentials; _ = encoding.GetCodec
)

func runMuxClient(ctx context.Context, opts ClientOpts, kind transportKind) error {
	var wg sync.WaitGroup
	for i := 0; i < opts.PoolSize; i++ { wg.Add(1); go func(id int) { defer wg.Done(); runMuxSessionLoop(ctx, opts, kind, id) }(i) }
	<-ctx.Done(); wg.Wait(); return nil
}

func runMuxSessionLoop(ctx context.Context, opts ClientOpts, kind transportKind, id int) {
	_ = id
	dialer := net.Dialer{Timeout: opts.DialTimeout}
	for {
		select { case <-ctx.Done(): return; default: }
		conn, err := dialControlConn(ctx, opts, kind, roleMux)
		if err != nil { time.Sleep(opts.RetryInterval); continue }
		sess := newSimpleMuxSession(conn)
		err = serveMuxClientSession(ctx, sess, dialer, opts)
		_ = sess.Close(); _ = conn.Close()
		if ctx.Err() != nil { return }
		if err != nil { time.Sleep(opts.RetryInterval) }
	}
}

func serveMuxClientSession(ctx context.Context, sess *simpleMuxSession, dialer net.Dialer, opts ClientOpts) error {
	go func() { <-ctx.Done(); _ = sess.Close() }()
	for {
		stream, err := sess.Accept(ctx)
		if err != nil { if ctx.Err() != nil { return nil }; return err }
		go func(s net.Conn) {
			br := bufio.NewReader(s)
			target, err := br.ReadString('\n')
			if err != nil { _ = s.Close(); return }
			target = strings.TrimSpace(target)
			if strings.HasPrefix(target, "UDP ") { _ = handleUDPClientRelay(prefixedConn{Conn:s, r:br}, strings.TrimSpace(strings.TrimPrefix(target,"UDP "))); _ = s.Close(); return }
			up, err := dialer.DialContext(ctx, "tcp", target)
			if err != nil { _ = s.Close(); return }
			applyTCPOptions(up, opts.NoDelay, opts.Keepalive); bridge(prefixedConn{Conn:s, r:br}, up); _ = up.Close(); _ = s.Close()
		}(stream)
	}
}

func dialControlConn(ctx context.Context, opts ClientOpts, kind transportKind, role string) (net.Conn, error) {
	var conn net.Conn; var err error
	switch { case kind.ws: conn,err=dialWSControl(ctx,opts,kind); case kind.grpc: conn,err=dialGRPCControl(ctx,opts,kind); case kind.http: conn,err=dialHTTPConnectControl(ctx,opts,kind); case kind.kcp: conn,err=dialKCPControl(ctx,opts,kind); case kind.udp: conn,err=dialUDPControl(ctx,opts,kind); default: conn,err=dialTCPControl(ctx,opts,kind) }
	if err != nil { return nil,err }
	if kind.slip { conn,err=wrapSlipstreamClient(conn); if err != nil { _=conn.Close(); return nil,err } }
	if _,err:=io.WriteString(conn,opts.Token+"\n"+role+"\n"); err!=nil { _=conn.Close(); return nil,err }
	return conn,nil
}

func dialUDPControl(ctx context.Context, opts ClientOpts, kind transportKind) (net.Conn,error) { d:=net.Dialer{Timeout:opts.DialTimeout}; remote:=opts.RemoteAddr; if kind.dns { remote=withDefaultPort(remote,"53") }; return d.DialContext(ctx,"udp",remote) }

func dialTCPControl(ctx context.Context, opts ClientOpts, kind transportKind) (net.Conn,error) {
	d:=net.Dialer{Timeout:opts.DialTimeout}
	if kind.tls { sni:=opts.TLSServerName; if sni=="" { sni=extractHost(opts.RemoteAddr) }; conn,err:=tls.DialWithDialer(&d,"tcp",opts.RemoteAddr,&tls.Config{InsecureSkipVerify:true,ServerName:sni}); if err!=nil{return nil,err}; applyTCPOptions(conn,opts.NoDelay,opts.Keepalive); return conn,nil }
	conn,err:=d.DialContext(ctx,"tcp",opts.RemoteAddr); if err!=nil{return nil,err}; applyTCPOptions(conn,opts.NoDelay,opts.Keepalive); return conn,nil
}

func dialWSControl(ctx context.Context, opts ClientOpts, kind transportKind) (net.Conn,error) {
	u,err:=buildWebsocketURL(opts.RemoteAddr,kind.tls); if err!=nil{return nil,err}; parsed,err:=url.Parse(u); if err!=nil{return nil,err}; hostPort:=parsed.Host; path:=parsed.EscapedPath(); if path==""{path="/"}
	d:=net.Dialer{Timeout:opts.DialTimeout}; var raw net.Conn
	if kind.tls { sni:=opts.TLSServerName; if sni==""{sni=extractHost(hostPort)}; raw,err=tls.DialWithDialer(&d,"tcp",hostPort,&tls.Config{InsecureSkipVerify:true,ServerName:sni}) } else { raw,err=d.DialContext(ctx,"tcp",hostPort) }
	if err!=nil{return nil,err}; br:=bufio.NewReader(raw); nonce:=make([]byte,16); if _,err:=rand.Read(nonce);err!=nil{_=raw.Close();return nil,err}; wsKey:=base64.StdEncoding.EncodeToString(nonce)
	req:="GET "+path+" HTTP/1.1\r\nHost: "+hostPort+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: "+wsKey+"\r\n\r\n"
	if _,err:=io.WriteString(raw,req);err!=nil{_=raw.Close();return nil,err}; status,err:=br.ReadString('\n');if err!=nil{_=raw.Close();return nil,err};if !strings.Contains(status,"101"){_=raw.Close();return nil,fmt.Errorf("websocket upgrade failed: %s",strings.TrimSpace(status))}
	for { line,err:=br.ReadString('\n');if err!=nil{_=raw.Close();return nil,err};if line=="\r\n"{break} }
	return newWSFrameConn(prefixedConn{Conn:raw,r:br},true),nil
}

func serveHTTPConnectControl(ctx context.Context, opts ServerOpts, kind transportKind, onConn func(net.Conn)) error {
	h:=http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){if r.Method!=http.MethodConnect{http.Error(w,"CONNECT required",http.StatusMethodNotAllowed);return};hj,ok:=w.(http.Hijacker);if !ok{http.Error(w,"hijack not supported",http.StatusInternalServerError);return};conn,rw,err:=hj.Hijack();if err!=nil{return};_,_=rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n");_=rw.Flush();go onConn(prefixedConn{Conn:conn,r:rw.Reader})})
	srv:=&http.Server{Addr:opts.BindAddr,Handler:h};go func(){<-ctx.Done();_=srv.Shutdown(context.Background())}();opts.Logger.Printf("http-connect control listener on %s",opts.BindAddr)
	if kind.tls { if opts.TLSCert==""||opts.TLSKey==""{return fmt.Errorf("https/httpsmux requires tls_cert and tls_key")};err:=srv.ListenAndServeTLS(opts.TLSCert,opts.TLSKey);if err!=nil&&!errors.Is(err,http.ErrServerClosed){return err};return nil }
	err:=srv.ListenAndServe();if err!=nil&&!errors.Is(err,http.ErrServerClosed){return err};return nil
}
