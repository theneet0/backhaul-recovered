package tunnel

import (
	"bufio"; "bytes"; "context"; "crypto/rand"; "crypto/sha1"; "crypto/sha256"; "crypto/tls"; "encoding/base64"; "encoding/binary"; "encoding/json"; "errors"; "fmt"; "io"; "log"; "net"; "net/http"; "net/url"; "strings"; "sync"; "sync/atomic"; "time"
	kcp "github.com/xtaci/kcp-go/v5"
	"google.golang.org/grpc"; "google.golang.org/grpc/credentials"; "google.golang.org/grpc/credentials/insecure"; "google.golang.org/grpc/encoding"
)
var (_=bufio.NewReader;_=bytes.NewBuffer;_=context.Background;_=rand.Reader;_=sha1.New;_=sha256.New;_=tls.VersionTLS13;_=base64.StdEncoding;_=binary.BigEndian;_=json.Marshal;_=errors.New;_=fmt.Sprintf;_=io.EOF;_=log.Default;_=net.IPv4len;_=http.MethodGet;_=url.URL{};_=strings.TrimSpace;_=sync.Mutex{};_=atomic.Bool{};_=time.Second;_=kcp.ListenWithOptions;_=grpc.NewServer;_ credentials.TransportCredentials;_=insecure.NewCredentials;_=encoding.GetCodec)

func dialHTTPConnectControl(ctx context.Context, opts ClientOpts, kind transportKind) (net.Conn, error) {
	hostPort, err := resolveHTTPControlHostPort(opts.RemoteAddr, kind.tls); if err != nil { return nil, err }
	d := net.Dialer{Timeout: opts.DialTimeout}; var raw net.Conn
	if kind.tls { sni:=opts.TLSServerName; if sni==""{sni=extractHost(hostPort)}; raw,err=tls.DialWithDialer(&d,"tcp",hostPort,&tls.Config{InsecureSkipVerify:true,ServerName:sni}) } else { raw,err=d.DialContext(ctx,"tcp",hostPort) }
	if err!=nil{return nil,err}; br:=bufio.NewReader(raw); req:="CONNECT "+hostPort+" HTTP/1.1\r\nHost: "+hostPort+"\r\nProxy-Connection: keep-alive\r\n\r\n"
	if _,err:=io.WriteString(raw,req);err!=nil{_=raw.Close();return nil,err}; status,err:=br.ReadString('\n');if err!=nil{_=raw.Close();return nil,err};if !strings.Contains(status,"200"){_=raw.Close();return nil,fmt.Errorf("http connect failed: %s",strings.TrimSpace(status))}
	for {line,err:=br.ReadString('\n');if err!=nil{_=raw.Close();return nil,err};if line=="\r\n"{break}}
	return prefixedConn{Conn:raw,r:br},nil
}
func listenKCPControl(addr string)(net.Listener,error){return kcp.ListenWithOptions(addr,nil,10,3)}
func dialKCPControl(ctx context.Context,opts ClientOpts,_ transportKind)(net.Conn,error){type out struct{c net.Conn;err error};ch:=make(chan out,1);go func(){c,err:=kcp.DialWithOptions(opts.RemoteAddr,nil,10,3);ch<-out{c,err}}();select{case<-ctx.Done():return nil,ctx.Err();case r:=<-ch:if r.err!=nil{return nil,r.err};tuneKCP(r.c);return r.c,nil}}
func tuneKCP(conn net.Conn){s,ok:=conn.(*kcp.UDPSession);if !ok{return};s.SetStreamMode(true);s.SetWriteDelay(false);s.SetNoDelay(1,20,2,1);s.SetMtu(1350);s.SetACKNoDelay(true)}

type grpcCodecJSON struct{}
func(grpcCodecJSON)Name()string{return "json"}
func(grpcCodecJSON)Marshal(v any)([]byte,error){return json.Marshal(v)}
func(grpcCodecJSON)Unmarshal(data []byte,v any)error{return json.Unmarshal(data,v)}
var grpcCodecOnce sync.Once
func ensureGRPCCodec(){grpcCodecOnce.Do(func(){encoding.RegisterCodec(grpcCodecJSON{})})}
type grpcChunk struct{Data []byte `json:"data,omitempty"`;EOF bool `json:"eof,omitempty"`}
type grpcStream interface{Context()context.Context;SendMsg(any)error;RecvMsg(any)error}
type grpcControlService interface{Handle(grpc.ServerStream)error}
type grpcControlServer struct{onConn func(net.Conn)}
func(s *grpcControlServer)Handle(stream grpc.ServerStream)error{conn:=newGRPCNetConn(stream,nil);go s.onConn(conn);<-stream.Context().Done();_=conn.Close();return nil}
func grpcControlHandler(srv any,stream grpc.ServerStream)error{return srv.(grpcControlService).Handle(stream)}
var grpcControlServiceDesc=grpc.ServiceDesc{ServiceName:"backhaul.Control",HandlerType:(*grpcControlService)(nil),Streams:[]grpc.StreamDesc{{StreamName:"Connect",Handler:grpcControlHandler,ServerStreams:true,ClientStreams:true}}}
func serveGRPCControl(ctx context.Context,opts ServerOpts,kind transportKind,onConn func(net.Conn))error{ensureGRPCCodec();var creds credentials.TransportCredentials;if kind.tls{if opts.TLSCert==""||opts.TLSKey==""{return fmt.Errorf("grpcs/grpcsmux requires tls_cert and tls_key")};cert,err:=tls.LoadX509KeyPair(opts.TLSCert,opts.TLSKey);if err!=nil{return err};creds=credentials.NewTLS(&tls.Config{Certificates:[]tls.Certificate{cert}})}else{creds=insecure.NewCredentials()};s:=grpc.NewServer(grpc.Creds(creds));s.RegisterService(&grpcControlServiceDesc,&grpcControlServer{onConn:onConn});ln,err:=net.Listen("tcp",opts.BindAddr);if err!=nil{return err};defer ln.Close();opts.Logger.Printf("grpc control listener on %s",opts.BindAddr);done:=make(chan struct{});go func(){defer close(done);_=s.Serve(ln)}();select{case<-ctx.Done():s.GracefulStop();<-done;return nil;case<-done:return fmt.Errorf("grpc server stopped")}}
func dialGRPCControl(ctx context.Context,opts ClientOpts,kind transportKind)(net.Conn,error){ensureGRPCCodec();target,err:=resolveGRPCTarget(opts.RemoteAddr,kind.tls);if err!=nil{return nil,err};var creds credentials.TransportCredentials;if kind.tls{sni:=opts.TLSServerName;if sni==""{sni=extractHost(target)};creds=credentials.NewTLS(&tls.Config{InsecureSkipVerify:true,ServerName:sni})}else{creds=insecure.NewCredentials()};cc,err:=grpc.DialContext(ctx,target,grpc.WithTransportCredentials(creds),grpc.WithBlock(),grpc.WithDefaultCallOptions(grpc.CallContentSubtype("json")));if err!=nil{return nil,err};desc:=&grpc.StreamDesc{StreamName:"Connect",ServerStreams:true,ClientStreams:true};cs,err:=cc.NewStream(ctx,desc,"/backhaul.Control/Connect");if err!=nil{_=cc.Close();return nil,err};return newGRPCNetConn(cs,cc.Close),nil}
type grpcNetConn struct{stream grpcStream;onClose func()error;rmu sync.Mutex;wmu sync.Mutex;rbuf bytes.Buffer;closed atomic.Bool}
func newGRPCNetConn(stream grpcStream,onClose func()error)net.Conn{return &grpcNetConn{stream:stream,onClose:onClose}}
func(c *grpcNetConn)Read(p []byte)(int,error){c.rmu.Lock();defer c.rmu.Unlock();for c.rbuf.Len()==0{var chunk grpcChunk;if err:=c.stream.RecvMsg(&chunk);err!=nil{return 0,err};if chunk.EOF{return 0,io.EOF};_,_=c.rbuf.Write(chunk.Data)};return c.rbuf.Read(p)}
func(c *grpcNetConn)Write(p []byte)(int,error){c.wmu.Lock();defer c.wmu.Unlock();if c.closed.Load(){return 0,net.ErrClosed};cp:=append([]byte(nil),p...);if err:=c.stream.SendMsg(&grpcChunk{Data:cp});err!=nil{return 0,err};return len(p),nil}
func(c *grpcNetConn)Close()error{if !c.closed.CompareAndSwap(false,true){return nil};_=c.stream.SendMsg(&grpcChunk{EOF:true});if cs,ok:=c.stream.(interface{CloseSend()error});ok{_=cs.CloseSend()};if c.onClose!=nil{return c.onClose()};return nil}
func(c *grpcNetConn)LocalAddr()net.Addr{return stringAddr{n:"grpc",a:"local"}}
