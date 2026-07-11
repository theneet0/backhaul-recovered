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

func wrapSlipstreamServer(conn net.Conn) (net.Conn, error) {
	var hello [4 + 16]byte
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	if _, err := io.ReadFull(conn, hello[:]); err != nil { return conn, err }
	if string(hello[:4]) != slipMagic { return conn, fmt.Errorf("invalid slipstream hello") }
	var serverNonce [16]byte
	if _, err := rand.Read(serverNonce[:]); err != nil { return conn, err }
	reply := make([]byte, 4+16); copy(reply[:4], []byte(slipMagic)); copy(reply[4:], serverNonce[:])
	if _, err := conn.Write(reply); err != nil { return conn, err }
	_ = conn.SetReadDeadline(time.Time{})
	key := sha256.Sum256(append(hello[4:20], serverNonce[:]...))
	return newSlipstreamConn(conn, key), nil
}
func wrapSlipstreamClient(conn net.Conn)(net.Conn,error){var clientNonce [16]byte;if _,err:=rand.Read(clientNonce[:]);err!=nil{return conn,err};hello:=make([]byte,20);copy(hello[:4],[]byte(slipMagic));copy(hello[4:],clientNonce[:]);_=conn.SetDeadline(time.Now().Add(8*time.Second));if _,err:=conn.Write(hello);err!=nil{return conn,err};var reply [20]byte;if _,err:=io.ReadFull(conn,reply[:]);err!=nil{return conn,err};if string(reply[:4])!=slipMagic{return conn,fmt.Errorf("invalid slipstream reply")};_=conn.SetDeadline(time.Time{});key:=sha256.Sum256(append(clientNonce[:],reply[4:20]...));return newSlipstreamConn(conn,key),nil}
type slipstreamConn struct{conn net.Conn;key [32]byte;rmu sync.Mutex;wmu sync.Mutex;rbuf bytes.Buffer;roff uint64;woff uint64}
func newSlipstreamConn(conn net.Conn,key [32]byte)net.Conn{return &slipstreamConn{conn:conn,key:key}}
func(s *slipstreamConn)Read(p []byte)(int,error){s.rmu.Lock();defer s.rmu.Unlock();for s.rbuf.Len()==0{var hdr [2]byte;if _,err:=io.ReadFull(s.conn,hdr[:]);err!=nil{return 0,err};n:=int(binary.BigEndian.Uint16(hdr[:]));if n==0{continue};payload:=make([]byte,n);if _,err:=io.ReadFull(s.conn,payload);err!=nil{return 0,err};s.xor(payload,s.roff);s.roff+=uint64(len(payload));_,_=s.rbuf.Write(payload)};return s.rbuf.Read(p)}
func(s *slipstreamConn)Write(p []byte)(int,error){s.wmu.Lock();defer s.wmu.Unlock();written:=0;for len(p)>0{chunk:=p;if len(chunk)>65535{chunk=p[:65535]};enc:=append([]byte(nil),chunk...);s.xor(enc,s.woff);s.woff+=uint64(len(enc));var hdr [2]byte;binary.BigEndian.PutUint16(hdr[:],uint16(len(enc)));if _,err:=s.conn.Write(hdr[:]);err!=nil{return written,err};if _,err:=s.conn.Write(enc);err!=nil{return written,err};written+=len(chunk);p=p[len(chunk):]};return written,nil}
func(s *slipstreamConn)xor(b []byte,offset uint64){for i:=range b{b[i]^=s.key[(offset+uint64(i))%uint64(len(s.key))]}}
func(s *slipstreamConn)Close()error{return s.conn.Close()}
func(s *slipstreamConn)LocalAddr()net.Addr{return s.conn.LocalAddr()}
func(s *slipstreamConn)RemoteAddr()net.Addr{return s.conn.RemoteAddr()}
func(s *slipstreamConn)SetDeadline(t time.Time)error{return s.conn.SetDeadline(t)}
func(s *slipstreamConn)SetReadDeadline(t time.Time)error{return s.conn.SetReadDeadline(t)}
func(s *slipstreamConn)SetWriteDeadline(t time.Time)error{return s.conn.SetWriteDeadline(t)}
type simpleMuxHolder struct{mu sync.Mutex;sessions []*simpleMuxSession;next int}
func(h *simpleMuxHolder)Add(s *simpleMuxSession){h.mu.Lock();defer h.mu.Unlock();h.sessions=append(h.sessions,s)}
func(h *simpleMuxHolder)Remove(s *simpleMuxSession){h.mu.Lock();defer h.mu.Unlock();out:=h.sessions[:0];for _,it:=range h.sessions{if it!=s&&!it.IsClosed(){out=append(out,it)}};h.sessions=out;if h.next>=len(h.sessions){h.next=0}}
func(h *simpleMuxHolder)OpenStream(ctx context.Context)(net.Conn,error){for{s:=h.nextSession();if s!=nil{stream,err:=s.OpenStream();if err==nil{return stream,nil};h.Remove(s)};select{case<-ctx.Done():return nil,ctx.Err();case<-time.After(120*time.Millisecond):}}}
func(h *simpleMuxHolder)nextSession()*simpleMuxSession{h.mu.Lock();defer h.mu.Unlock();if len(h.sessions)==0{return nil};for i:=0;i<len(h.sessions);i++{idx:=(h.next+i)%len(h.sessions);s:=h.sessions[idx];if s==nil||s.IsClosed(){continue};h.next=(idx+1)%len(h.sessions);return s};return nil}
type simpleMuxSession struct{conn net.Conn;writeMu sync.Mutex;streamsMu sync.Mutex;streams map[uint32]*simpleMuxStream;acceptCh chan *simpleMuxStream;nextID atomic.Uint32;closed atomic.Bool;done chan struct{}}
func newSimpleMuxSession(conn net.Conn)*simpleMuxSession{s:=&simpleMuxSession{conn:conn,streams:make(map[uint32]*simpleMuxStream),acceptCh:make(chan *simpleMuxStream,1024),done:make(chan struct{})};s.nextID.Store(1);go s.readLoop();return s}
func(s *simpleMuxSession)Done()<-chan struct{}{return s.done}
func(s *simpleMuxSession)IsClosed()bool{return s.closed.Load()}
func(s *simpleMuxSession)Close()error{if s.closed.Swap(true){return nil};close(s.done);_=s.conn.Close();s.streamsMu.Lock();for _,st:=range s.streams{st.remoteClose()};s.streams=map[uint32]*simpleMuxStream{};s.streamsMu.Unlock();return nil}
func(s *simpleMuxSession)Accept(ctx context.Context)(net.Conn,error){select{case st:=<-s.acceptCh:return st,nil;case<-s.done:return nil,io.EOF;case<-ctx.Done():return nil,ctx.Err()}}
func(s *simpleMuxSession)OpenStream()(net.Conn,error){if s.IsClosed(){return nil,io.EOF};id:=s.nextID.Add(1);st:=newSimpleMuxStream(s,id);s.streamsMu.Lock();s.streams[id]=st;s.streamsMu.Unlock();if err:=s.writeFrame(frameOpen,id,nil);err!=nil{s.removeStream(id);return nil,err};return st,nil}
