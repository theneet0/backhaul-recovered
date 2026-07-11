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

var (_=bufio.NewReader;_=bytes.NewBuffer;_=context.Background;_=rand.Reader;_=sha1.New;_=sha256.New;_=tls.VersionTLS13;_=base64.StdEncoding;_=binary.BigEndian;_=json.Marshal;_=errors.New;_=fmt.Sprintf;_=io.EOF;_=log.Default;_=net.IPv4len;_=http.MethodGet;_=url.URL{};_=strings.TrimSpace;_=sync.Mutex{};_=atomic.Bool{};_=time.Second;_=kcp.ListenWithOptions;_=grpc.NewServer;_ credentials.TransportCredentials;_=insecure.NewCredentials;_=encoding.GetCodec)

func startPublicUDPListener(ctx context.Context, m PortMapping, lg *log.Logger, acquire func(context.Context, string) (net.Conn, error)) error {
	udpAddr, err := net.ResolveUDPAddr("udp", m.ListenAddr); if err != nil { return fmt.Errorf("resolve udp %s: %w", m.ListenAddr, err) }
	ln, err := net.ListenUDP("udp", udpAddr); if err != nil { return fmt.Errorf("listen udp %s: %w", m.ListenAddr, err) }
	lg.Printf("public udp listener %s -> %s", m.ListenAddr, m.TargetAddr)
	go func(){<-ctx.Done();_=ln.Close()}()
	var assocMu sync.Mutex; assocs:=map[string]*udpAssociation{}; buf:=make([]byte,64*1024)
	for {
		n,src,err:=ln.ReadFromUDP(buf);if err!=nil{if ctx.Err()!=nil{return nil};return fmt.Errorf("udp read error on %s: %w",m.ListenAddr,err)}
		key:=src.String();assocMu.Lock();assoc:=assocs[key];assocMu.Unlock()
		if assoc==nil{
			reqCtx,cancel:=context.WithTimeout(ctx,12*time.Second);c,err:=acquire(reqCtx,m.TargetAddr);cancel();if err!=nil{continue}
			assoc=&udpAssociation{conn:c,addr:src};assocMu.Lock();assocs[key]=assoc;assocMu.Unlock()
			go func(k string,a *udpAssociation){defer func(){_=a.conn.Close();assocMu.Lock();delete(assocs,k);assocMu.Unlock()}();for{pkt,err:=readDatagramFrame(a.conn);if err!=nil{return};_,_=ln.WriteToUDP(pkt,a.addr)}}(key,assoc)
		}
		assoc.mu.Lock();err=writeDatagramFrame(assoc.conn,buf[:n]);assoc.mu.Unlock();if err!=nil{_=assoc.conn.Close()}
	}
}

func bridge(a,b net.Conn){var wg sync.WaitGroup;wg.Add(2);go func(){defer wg.Done();_,_=io.Copy(a,b);_=a.SetReadDeadline(time.Now())}();go func(){defer wg.Done();_,_=io.Copy(b,a);_=b.SetReadDeadline(time.Now())}();wg.Wait()}
func handleUDPClientRelay(control net.Conn,target string)error{up,err:=net.Dial("udp",target);if err!=nil{return err};defer up.Close();errCh:=make(chan error,2);go func(){for{pkt,err:=readDatagramFrame(control);if err!=nil{errCh<-err;return};if _,err:=up.Write(pkt);err!=nil{errCh<-err;return}}}();go func(){buf:=make([]byte,64*1024);for{n,err:=up.Read(buf);if err!=nil{errCh<-err;return};if err:=writeDatagramFrame(control,buf[:n]);err!=nil{errCh<-err;return}}}();return<-errCh}
func writeDatagramFrame(w io.Writer,p []byte)error{if len(p)>65535{p=p[:65535]};hdr:=[]byte{0,0};binary.BigEndian.PutUint16(hdr,uint16(len(p)));if _,err:=w.Write(hdr);err!=nil{return err};_,err:=w.Write(p);return err}
func readDatagramFrame(r io.Reader)([]byte,error){hdr:=make([]byte,2);if _,err:=io.ReadFull(r,hdr);err!=nil{return nil,err};n:=int(binary.BigEndian.Uint16(hdr));if n==0{return []byte{},nil};buf:=make([]byte,n);_,err:=io.ReadFull(r,buf);return buf,err}
type prefixedConn struct{net.Conn;r *bufio.Reader}
func(c prefixedConn)Read(p []byte)(int,error){return c.r.Read(p)}
func ValidateTransport(raw string)error{_,err:=parseTransport(raw);return err}
func parseTransport(raw string)(transportKind,error){t:=strings.ToLower(strings.TrimSpace(raw));switch t{case "","tcp":return transportKind{},nil;case "kcp":return transportKind{kcp:true},nil;case "kcpmux","xkcpmux":return transportKind{kcp:true,mux:true},nil;case "grpc":return transportKind{grpc:true},nil;case "grpcs":return transportKind{grpc:true,tls:true},nil;case "grpcmux","xgrpcmux":return transportKind{grpc:true,mux:true},nil;case "grpcsmux","xgrpcsmux":return transportKind{grpc:true,tls:true,mux:true},nil;case "http":return transportKind{http:true},nil;case "https":return transportKind{http:true,tls:true},nil;case "httpmux","xhttpmux":return transportKind{http:true,mux:true},nil;case "httpsmux","xhttpsmux":return transportKind{http:true,tls:true,mux:true},nil;case "udp":return transportKind{udp:true},nil;case "udpmux","xudpmux":return transportKind{udp:true,mux:true},nil;case "dns":return transportKind{udp:true,dns:true},nil;case "dnsmux","xdnsmux":return transportKind{udp:true,dns:true,mux:true},nil;case "slipstream","slip","sstream":return transportKind{slip:true},nil;case "slipstreammux","slipmux","sstreammux":return transportKind{slip:true,mux:true},nil;case "raw","rawsocket","socketraw":return transportKind{},nil;case "tun":return transportKind{mux:true},nil;case "anytls":return transportKind{tls:true},nil;case "tcpmux","xtcpmux":return transportKind{mux:true},nil;case "ws":return transportKind{ws:true},nil;case "wss":return transportKind{ws:true,tls:true},nil;case "wsmux","xwsmux":return transportKind{ws:true,mux:true},nil;case "wssmux":return transportKind{ws:true,tls:true,mux:true},nil;default:return transportKind{},fmt.Errorf("unsupported transport type: %s",raw)}}
func withDefaultPort(addr,defaultPort string)string{if addr==""{return ":"+defaultPort};if _,_,err:=net.SplitHostPort(addr);err==nil{return addr};if strings.HasPrefix(addr,":"){return addr};if strings.Count(addr,":")>1&&!strings.HasPrefix(addr,"["){return "["+addr+"]:"+defaultPort};return addr+":"+defaultPort}
func resolveHTTPControlHostPort(remote string,secure bool)(string,error){if strings.HasPrefix(remote,"http://")||strings.HasPrefix(remote,"https://"){u,err:=url.Parse(remote);if err!=nil{return "",err};if u.Host==""{return "",fmt.Errorf("invalid remote_addr for http: %s",remote)};if _,_,err:=net.SplitHostPort(u.Host);err!=nil{if secure{return net.JoinHostPort(u.Host,"443"),nil};return net.JoinHostPort(u.Host,"80"),nil};return u.Host,nil};if _,_,err:=net.SplitHostPort(remote);err==nil{return remote,nil};if secure{return withDefaultPort(remote,"443"),nil};return withDefaultPort(remote,"80"),nil}
