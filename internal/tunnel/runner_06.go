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

func resolveGRPCTarget(remote string, secure bool) (string, error) {
	if strings.HasPrefix(remote,"grpc://")||strings.HasPrefix(remote,"grpcs://"){u,err:=url.Parse(remote);if err!=nil{return "",err};if u.Host==""{return "",fmt.Errorf("invalid remote_addr for grpc: %s",remote)};if _,_,err:=net.SplitHostPort(u.Host);err==nil{return u.Host,nil};if secure{return net.JoinHostPort(u.Host,"443"),nil};return net.JoinHostPort(u.Host,"80"),nil}
	if _,_,err:=net.SplitHostPort(remote);err==nil{return remote,nil};if secure{return withDefaultPort(remote,"443"),nil};return withDefaultPort(remote,"80"),nil
}
type stringAddr struct{n string;a string}
func(a stringAddr)Network()string{return a.n}
func(a stringAddr)String()string{return a.a}
func applyTCPOptions(conn net.Conn,noDelay bool,keepalive time.Duration){tc,ok:=conn.(*net.TCPConn);if !ok{return};_=tc.SetNoDelay(noDelay);if keepalive>0{_=tc.SetKeepAlive(true);_=tc.SetKeepAlivePeriod(keepalive)}}
func buildWebsocketURL(addr string,secure bool)(string,error){if strings.HasPrefix(addr,"ws://")||strings.HasPrefix(addr,"wss://"){u,err:=url.Parse(addr);if err!=nil{return "",err};if u.Path==""{u.Path="/"};return u.String(),nil};scheme:="ws";if secure{scheme="wss"};u,err:=url.Parse(scheme+"://"+addr);if err!=nil{return "",err};if u.Host==""{return "",fmt.Errorf("invalid remote_addr for websocket: %s",addr)};if u.Path==""{u.Path="/"};return u.String(),nil}
func extractHost(addr string)string{h:=addr;if strings.Contains(addr,"://"){if u,err:=url.Parse(addr);err==nil{h=u.Host}};if host,_,err:=net.SplitHostPort(h);err==nil{return host};return h}
func headerContainsToken(v,token string)bool{token=strings.ToLower(strings.TrimSpace(token));for _,p:=range strings.Split(v,","){if strings.ToLower(strings.TrimSpace(p))==token{return true}};return false}
func websocketAccept(k string)string{const wsGUID="258EAFA5-E914-47DA-95CA-C5AB0DC85B11";sum:=sha1.Sum([]byte(k+wsGUID));return base64.StdEncoding.EncodeToString(sum[:])}
type wsFrameConn struct{conn net.Conn;isClient bool;wmu sync.Mutex;rmu sync.Mutex;rbuf bytes.Buffer}
func newWSFrameConn(c net.Conn,isClient bool)net.Conn{return &wsFrameConn{conn:c,isClient:isClient}}
func(w *wsFrameConn)Read(p []byte)(int,error){w.rmu.Lock();defer w.rmu.Unlock();for w.rbuf.Len()==0{if err:=w.readNextDataFrame();err!=nil{return 0,err}};return w.rbuf.Read(p)}
func(w *wsFrameConn)Write(p []byte)(int,error){w.wmu.Lock();defer w.wmu.Unlock();const maxPayload=65535;written:=0;for len(p)>0{chunk:=p;if len(chunk)>maxPayload{chunk=p[:maxPayload]};if err:=w.writeFrame(0x2,chunk);err!=nil{return written,err};written+=len(chunk);p=p[len(chunk):]};return written,nil}
func(w *wsFrameConn)Close()error{_=w.writeFrame(0x8,nil);return w.conn.Close()}
func(w *wsFrameConn)LocalAddr()net.Addr{return w.conn.LocalAddr()}
func(w *wsFrameConn)RemoteAddr()net.Addr{return w.conn.RemoteAddr()}
func(w *wsFrameConn)SetDeadline(t time.Time)error{return w.conn.SetDeadline(t)}
func(w *wsFrameConn)SetReadDeadline(t time.Time)error{return w.conn.SetReadDeadline(t)}
func(w *wsFrameConn)SetWriteDeadline(t time.Time)error{return w.conn.SetWriteDeadline(t)}
func(w *wsFrameConn)readNextDataFrame()error{var h [2]byte;if _,err:=io.ReadFull(w.conn,h[:]);err!=nil{return err};opcode:=h[0]&0x0F;hasMask:=(h[1]&0x80)!=0;n,err:=wsReadLen(w.conn,h[1]&0x7F);if err!=nil{return err};var maskKey [4]byte;if hasMask{if _,err:=io.ReadFull(w.conn,maskKey[:]);err!=nil{return err}};payload:=make([]byte,n);if _,err:=io.ReadFull(w.conn,payload);err!=nil{return err};if hasMask{for i:=range payload{payload[i]^=maskKey[i%4]}};switch opcode{case 0x1,0x2,0x0:_,_=w.rbuf.Write(payload);return nil;case 0x8:return io.EOF;case 0x9:_=w.writeFrame(0xA,payload);return nil;case 0xA:return nil;default:return nil}}
func(w *wsFrameConn)writeFrame(opcode byte,payload []byte)error{const fin=0x80;maskBit:=byte(0);if w.isClient{maskBit=0x80};hdr:=[]byte{fin|(opcode&0x0F)};n:=len(payload);switch{case n<126:hdr=append(hdr,maskBit|byte(n));case n<=65535:hdr=append(hdr,maskBit|126,byte(n>>8),byte(n));default:hdr=append(hdr,maskBit|127,byte(uint64(n)>>56),byte(uint64(n)>>48),byte(uint64(n)>>40),byte(uint64(n)>>32),byte(uint64(n)>>24),byte(uint64(n)>>16),byte(uint64(n)>>8),byte(uint64(n)))};if _,err:=w.conn.Write(hdr);err!=nil{return err};if w.isClient{var mk [4]byte;if _,err:=rand.Read(mk[:]);err!=nil{return err};if _,err:=w.conn.Write(mk[:]);err!=nil{return err};masked:=append([]byte(nil),payload...);for i:=range masked{masked[i]^=mk[i%4]};_,err:=w.conn.Write(masked);return err};if len(payload)==0{return nil};_,err:=w.conn.Write(payload);return err}
func wsReadLen(r io.Reader,n0 byte)(int,error){switch n0{case 126:var b [2]byte;if _,err:=io.ReadFull(r,b[:]);err!=nil{return 0,err};return int(binary.BigEndian.Uint16(b[:])),nil;case 127:var b [8]byte;if _,err:=io.ReadFull(r,b[:]);err!=nil{return 0,err};n:=binary.BigEndian.Uint64(b[:]);if n>(1<<31-1){return 0,fmt.Errorf("websocket frame too large: %d",n)};return int(n),nil;default:return int(n0),nil}}
