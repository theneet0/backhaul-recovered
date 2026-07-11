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

func (s *simpleMuxSession) readLoop(){hdr:=make([]byte,9);for{if _,err:=io.ReadFull(s.conn,hdr);err!=nil{_=s.Close();return};ft:=hdr[0];id:=binary.BigEndian.Uint32(hdr[1:5]);n:=binary.BigEndian.Uint32(hdr[5:9]);var payload []byte;if n>0{payload=make([]byte,n);if _,err:=io.ReadFull(s.conn,payload);err!=nil{_=s.Close();return}};s.handleFrame(ft,id,payload)}}
func(s *simpleMuxSession)handleFrame(ft byte,id uint32,payload []byte){switch ft{case frameOpen:st:=newSimpleMuxStream(s,id);s.streamsMu.Lock();s.streams[id]=st;s.streamsMu.Unlock();select{case s.acceptCh<-st:default:_=st.Close()};case frameData:st:=s.getStream(id);if st==nil{return};st.pushData(payload);case frameClose:st:=s.getStream(id);if st==nil{return};st.remoteClose();s.removeStream(id)}}
func(s *simpleMuxSession)writeFrame(ft byte,id uint32,payload []byte)error{if s.IsClosed(){return io.EOF};s.writeMu.Lock();defer s.writeMu.Unlock();hdr:=make([]byte,9);hdr[0]=ft;binary.BigEndian.PutUint32(hdr[1:5],id);binary.BigEndian.PutUint32(hdr[5:9],uint32(len(payload)));if _,err:=s.conn.Write(hdr);err!=nil{return err};if len(payload)>0{_,err:=s.conn.Write(payload);return err};return nil}
func(s *simpleMuxSession)getStream(id uint32)*simpleMuxStream{s.streamsMu.Lock();defer s.streamsMu.Unlock();return s.streams[id]}
func(s *simpleMuxSession)removeStream(id uint32){s.streamsMu.Lock();defer s.streamsMu.Unlock();delete(s.streams,id)}
type simpleMuxStream struct{sess *simpleMuxSession;id uint32;readCh chan []byte;readBuf bytes.Buffer;readMu sync.Mutex;readCond *sync.Cond;readClosed bool;closeOnce sync.Once;localClosed atomic.Bool;deadlineRead atomic.Value;deadlineWri atomic.Value}
func newSimpleMuxStream(sess *simpleMuxSession,id uint32)*simpleMuxStream{st:=&simpleMuxStream{sess:sess,id:id,readCh:make(chan []byte,256)};st.readCond=sync.NewCond(&st.readMu);go st.ingestLoop();return st}
func(s *simpleMuxStream)ingestLoop(){for p:=range s.readCh{s.readMu.Lock();_,_=s.readBuf.Write(p);s.readCond.Broadcast();s.readMu.Unlock()};s.readMu.Lock();s.readClosed=true;s.readCond.Broadcast();s.readMu.Unlock()}
func(s *simpleMuxStream)pushData(p []byte){if len(p)==0{return};cp:=append([]byte(nil),p...);select{case s.readCh<-cp:default:_=s.Close()}}
func(s *simpleMuxStream)remoteClose(){s.closeOnce.Do(func(){close(s.readCh)})}
func(s *simpleMuxStream)Read(p []byte)(int,error){s.readMu.Lock();defer s.readMu.Unlock();for s.readBuf.Len()==0&&!s.readClosed{s.readCond.Wait()};if s.readBuf.Len()==0&&s.readClosed{return 0,io.EOF};return s.readBuf.Read(p)}
func(s *simpleMuxStream)Write(p []byte)(int,error){if s.localClosed.Load()||s.sess.IsClosed(){return 0,io.EOF};const maxChunk=32*1024;written:=0;for len(p)>0{chunk:=p;if len(chunk)>maxChunk{chunk=p[:maxChunk]};if err:=s.sess.writeFrame(frameData,s.id,chunk);err!=nil{return written,err};written+=len(chunk);p=p[len(chunk):]};return written,nil}
func(s *simpleMuxStream)Close()error{if s.localClosed.Swap(true){return nil};s.sess.removeStream(s.id);_=s.sess.writeFrame(frameClose,s.id,nil);s.remoteClose();return nil}
func(s *simpleMuxStream)LocalAddr()net.Addr{return s.sess.conn.LocalAddr()}
func(s *simpleMuxStream)RemoteAddr()net.Addr{return s.sess.conn.RemoteAddr()}
func(s *simpleMuxStream)SetDeadline(t time.Time)error{s.deadlineRead.Store(t);s.deadlineWri.Store(t);return nil}
func(s *simpleMuxStream)SetReadDeadline(t time.Time)error{s.deadlineRead.Store(t);return nil}
func(s *simpleMuxStream)SetWriteDeadline(t time.Time)error{s.deadlineWri.Store(t);return nil}
