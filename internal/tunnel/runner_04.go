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

func (c *grpcNetConn) RemoteAddr() net.Addr { return stringAddr{n: "grpc", a: "remote"} }

func (c *grpcNetConn) SetDeadline(_ time.Time) error { return nil }
func (c *grpcNetConn) SetReadDeadline(_ time.Time) error { return nil }
func (c *grpcNetConn) SetWriteDeadline(_ time.Time) error { return nil }

func authenticateControlConn(conn net.Conn, token string, readTimeout time.Duration) (net.Conn, string, error) {
	if readTimeout > 0 { _ = conn.SetReadDeadline(time.Now().Add(readTimeout)) }
	br := bufio.NewReader(conn)
	tok, err := br.ReadString('\n'); if err != nil { return nil, "", err }
	role, err := br.ReadString('\n'); if err != nil { return nil, "", err }
	_ = conn.SetReadDeadline(time.Time{})
	tok = strings.TrimSpace(tok)
	if token != "" && tok != token { return nil, "", fmt.Errorf("invalid token") }
	return prefixedConn{Conn: conn, r: br}, strings.TrimSpace(role), nil
}

func listenTCPControl(addr string, kind transportKind, certPath, keyPath string) (net.Listener, error) {
	if !kind.tls { return net.Listen("tcp", addr) }
	if certPath == "" || keyPath == "" { return nil, fmt.Errorf("anytls requires tls_cert and tls_key") }
	cert, err := tls.LoadX509KeyPair(certPath, keyPath); if err != nil { return nil, err }
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	return tls.Listen("tcp", addr, cfg)
}

type udpControlListener struct { pc *net.UDPConn; acceptC chan net.Conn; closed chan struct{}; closeW sync.Once; mu sync.Mutex; conns map[string]*udpControlConn }

func listenUDPControl(addr string) (net.Listener, error) {
	ua, err := net.ResolveUDPAddr("udp", addr); if err != nil { return nil, err }
	pc, err := net.ListenUDP("udp", ua); if err != nil { return nil, err }
	l := &udpControlListener{pc: pc, acceptC: make(chan net.Conn, 256), closed: make(chan struct{}), conns: map[string]*udpControlConn{}}
	go l.readLoop(); return l, nil
}

func (l *udpControlListener) readLoop() {
	buf := make([]byte, 64*1024)
	for {
		n, raddr, err := l.pc.ReadFromUDP(buf); if err != nil { return }
		pkt := append([]byte(nil), buf[:n]...); key := raddr.String()
		l.mu.Lock(); c := l.conns[key]
		if c == nil {
			c = &udpControlConn{parent:l, remote:raddr, key:key, incoming:make(chan []byte,512), closed:make(chan struct{})}; l.conns[key]=c
			select { case l.acceptC <- c: default: delete(l.conns,key); close(c.closed); l.mu.Unlock(); continue }
		}
		l.mu.Unlock(); if !c.push(pkt) { _ = c.Close() }
	}
}
func (l *udpControlListener) Accept()(net.Conn,error){select{case<-l.closed:return nil,net.ErrClosed;case c:=<-l.acceptC:return c,nil}}
func (l *udpControlListener) Close() error { var err error; l.closeW.Do(func(){close(l.closed);err=l.pc.Close();l.mu.Lock();for _,c:=range l.conns{_=c.closeLocal()};l.conns=map[string]*udpControlConn{};l.mu.Unlock()});return err }
func (l *udpControlListener) Addr() net.Addr{return l.pc.LocalAddr()}
func (l *udpControlListener) remove(key string){l.mu.Lock();delete(l.conns,key);l.mu.Unlock()}

type udpControlConn struct{parent *udpControlListener;remote *net.UDPAddr;key string;incoming chan []byte;closed chan struct{};closeW sync.Once;rmu sync.Mutex;rbuf bytes.Buffer;wmu sync.Mutex}
func(c *udpControlConn)push(pkt []byte)bool{select{case<-c.closed:return false;case c.incoming<-pkt:return true;default:return false}}
func(c *udpControlConn)Read(p []byte)(int,error){c.rmu.Lock();defer c.rmu.Unlock();for c.rbuf.Len()==0{select{case<-c.closed:return 0,io.EOF;case pkt:=<-c.incoming:_,_=c.rbuf.Write(pkt)}};return c.rbuf.Read(p)}
func(c *udpControlConn)Write(p []byte)(int,error){c.wmu.Lock();defer c.wmu.Unlock();select{case<-c.closed:return 0,net.ErrClosed;default:};return c.parent.pc.WriteToUDP(p,c.remote)}
func(c *udpControlConn)closeLocal()error{c.closeW.Do(func(){close(c.closed);c.parent.remove(c.key)});return nil}
func(c *udpControlConn)Close()error{return c.closeLocal()}
func(c *udpControlConn)LocalAddr()net.Addr{return c.parent.pc.LocalAddr()}
func(c *udpControlConn)RemoteAddr()net.Addr{return c.remote}
func(c *udpControlConn)SetDeadline(_ time.Time)error{return nil}
func(c *udpControlConn)SetReadDeadline(_ time.Time)error{return nil}
func(c *udpControlConn)SetWriteDeadline(_ time.Time)error{return nil}

func startPublicListener(ctx context.Context,m PortMapping,lg *log.Logger,acquire func(context.Context,string)(net.Conn,error))error{
	ln,err:=net.Listen("tcp",m.ListenAddr);if err!=nil{return fmt.Errorf("listen %s: %w",m.ListenAddr,err)};lg.Printf("public listener %s -> %s",m.ListenAddr,m.TargetAddr)
	go func(){<-ctx.Done();_=ln.Close()}()
	go func(){for{in,err:=ln.Accept();if err!=nil{return};go func(c net.Conn){defer c.Close();reqCtx,cancel:=context.WithTimeout(ctx,12*time.Second);defer cancel();backend,err:=acquire(reqCtx,m.TargetAddr);if err!=nil{return};defer backend.Close();bridge(c,backend)}(in)}}()
	return nil
}

type udpAssociation struct{conn net.Conn;addr *net.UDPAddr;mu sync.Mutex}
