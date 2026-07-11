package tunnel

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"testing"
	"time"
)

func TestOriginalTypedStringFrame(t *testing.T) {
	var frame bytes.Buffer
	if err := writeOriginalTypedString(&frame, originalSignalHello, "secret"); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	want := []byte{0x00, 0x06, 0x06, 's', 'e', 'c', 'r', 'e', 't'}
	if !bytes.Equal(frame.Bytes(), want) {
		t.Fatalf("unexpected frame: got %x want %x", frame.Bytes(), want)
	}

	value, signal, err := readOriginalTypedString(&frame)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if value != "secret" || signal != originalSignalHello {
		t.Fatalf("decoded value=%q signal=%#x", value, signal)
	}
}

func TestNormalizeOriginalTarget(t *testing.T) {
	tests := map[string]string{
		"39082":           "127.0.0.1:39082",
		":39082":          "127.0.0.1:39082",
		"localhost:39082": "localhost:39082",
		"[::1]:39082":     "[::1]:39082",
	}
	for input, want := range tests {
		got, err := normalizeOriginalTarget(input)
		if err != nil {
			t.Fatalf("normalize %q: %v", input, err)
		}
		if got != want {
			t.Fatalf("normalize %q: got %q want %q", input, got, want)
		}
	}

	for _, input := range []string{"", "0", "65536", "localhost"} {
		if _, err := normalizeOriginalTarget(input); err == nil {
			t.Fatalf("normalize %q: expected an error", input)
		}
	}
}

func TestOriginalWireTarget(t *testing.T) {
	tests := map[string]string{
		"127.0.0.1:39082": "39082",
		"localhost:39082": "39082",
		"[::1]:39082":     "39082",
		"10.0.0.8:39082":  "10.0.0.8:39082",
	}
	for input, want := range tests {
		if got := originalWireTarget(input); got != want {
			t.Fatalf("wire target %q: got %q want %q", input, got, want)
		}
	}
}

func TestOriginalTCPProtocolEndToEnd(t *testing.T) {
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoListener.Close()
	go func() {
		for {
			conn, err := echoListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	controlAddr := reserveTCPAddr(t)
	publicAddr := reserveTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := log.New(io.Discard, "", 0)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- RunServer(ctx, ServerOpts{
			BindAddr:          controlAddr,
			Transport:         "tcp",
			Token:             "secret",
			Mappings:          []PortMapping{{ListenAddr: publicAddr, TargetAddr: echoListener.Addr().String()}},
			NoDelay:           true,
			Keepalive:         time.Second,
			HeartbeatInterval: 100 * time.Millisecond,
			HeartbeatTimeout:  time.Second,
			Logger:            logger,
		})
	}()

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- RunClient(ctx, ClientOpts{
			RemoteAddr:        controlAddr,
			Transport:         "tcp",
			Token:             "secret",
			DialTimeout:       time.Second,
			RetryInterval:     20 * time.Millisecond,
			PoolSize:          2,
			NoDelay:           true,
			Keepalive:         time.Second,
			HeartbeatInterval: 100 * time.Millisecond,
			HeartbeatTimeout:  time.Second,
			Logger:            logger,
		})
	}()

	payload := []byte("recovered-protocol-round-trip")
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", publicAddr, 250*time.Millisecond)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			if _, err = conn.Write(payload); err == nil {
				got := make([]byte, len(payload))
				_, err = io.ReadFull(conn, got)
				if err == nil && bytes.Equal(got, payload) {
					_ = conn.Close()
					break
				}
			}
			_ = conn.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("end-to-end relay did not become ready: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
	select {
	case err := <-clientErr:
		if err != nil {
			t.Fatalf("client shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not stop")
	}
}

func reserveTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}
