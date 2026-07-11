package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// These values were recovered from the v2.0.0-hotfix8 TCP wire format.
// Frames containing strings are: uint16(big endian) length, one signal byte,
// then the string bytes. Control messages are one signal byte.
const (
	originalSignalClosed    byte = 0x00
	originalSignalHeartbeat byte = 0x01
	originalSignalChannel   byte = 0x02
	originalSignalRTT       byte = 0x05
	originalSignalHello     byte = 0x06
	originalSignalTCP       byte = 0x10
	originalSignalUDP       byte = 0x11
)

func usesOriginalTCPProtocol(transport string) bool {
	return strings.EqualFold(strings.TrimSpace(transport), "tcp")
}

func writeOriginalTypedString(w io.Writer, signal byte, value string) error {
	if len(value) > 65535 {
		return fmt.Errorf("protocol string is too long: %d", len(value))
	}
	frame := make([]byte, 3+len(value))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(value)))
	frame[2] = signal
	copy(frame[3:], value)
	return writeOriginalFull(w, frame)
}

func readOriginalTypedString(r io.Reader) (string, byte, error) {
	var header [3]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return "", 0, err
	}
	n := binary.BigEndian.Uint16(header[:2])
	payload := make([]byte, int(n))
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", 0, err
	}
	return string(payload), header[2], nil
}

func writeOriginalSignal(w io.Writer, signal byte) error {
	return writeOriginalFull(w, []byte{signal})
}

func readOriginalSignal(r io.Reader) (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(r, b[:])
	return b[0], err
}

func normalizeOriginalTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("empty original protocol target")
	}

	if strings.HasPrefix(target, ":") {
		port := strings.TrimPrefix(target, ":")
		if err := validateOriginalPort(port); err != nil {
			return "", err
		}
		return net.JoinHostPort("127.0.0.1", port), nil
	}
	if !strings.Contains(target, ":") {
		if err := validateOriginalPort(target); err != nil {
			return "", err
		}
		return net.JoinHostPort("127.0.0.1", target), nil
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		return "", fmt.Errorf("invalid original protocol target %q: %w", target, err)
	}
	return target, nil
}

func validateOriginalPort(port string) error {
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return fmt.Errorf("invalid original protocol port %q", port)
	}
	return nil
}

func originalWireTarget(target string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(target))
	if err == nil && (host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return port
	}
	return target
}

func writeOriginalFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		p = p[n:]
	}
	return nil
}

type originalControl struct {
	conn net.Conn
	wmu  sync.Mutex
	once sync.Once
}

func (c *originalControl) writeSignal(signal byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeOriginalSignal(c.conn, signal)
}

func (c *originalControl) close() {
	c.once.Do(func() { _ = c.conn.Close() })
}

type originalServerState struct {
	ctx         context.Context
	token       string
	readTimeout time.Duration
	workers     chan net.Conn

	handshakeMu sync.Mutex
	controlMu   sync.RWMutex
	control     *originalControl
}

func newOriginalServerState(ctx context.Context, token string, readTimeout time.Duration) *originalServerState {
	if readTimeout <= 0 {
		readTimeout = 25 * time.Second
	}
	return &originalServerState{
		ctx:         ctx,
		token:       token,
		readTimeout: readTimeout,
		workers:     make(chan net.Conn, 512),
	}
}

func (s *originalServerState) currentControl() *originalControl {
	s.controlMu.RLock()
	defer s.controlMu.RUnlock()
	return s.control
}

func (s *originalServerState) setControl(c *originalControl) bool {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if s.control != nil {
		return false
	}
	s.control = c
	return true
}

func (s *originalServerState) clearControl(c *originalControl) {
	s.controlMu.Lock()
	if s.control == c {
		s.control = nil
	}
	s.controlMu.Unlock()
	c.close()
}

func (s *originalServerState) handleAccepted(conn net.Conn, opts ServerOpts) {
	applyTCPOptions(conn, opts.NoDelay, opts.Keepalive)
	if s.currentControl() != nil {
		s.enqueueWorker(conn)
		return
	}

	s.handshakeMu.Lock()
	defer s.handshakeMu.Unlock()
	if s.currentControl() != nil {
		s.enqueueWorker(conn)
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(s.readTimeout))
	token, signal, err := readOriginalTypedString(conn)
	if err != nil || signal != originalSignalHello || token != s.token {
		_ = conn.Close()
		return
	}
	if err := writeOriginalTypedString(conn, originalSignalChannel, s.token); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	control := &originalControl{conn: conn}
	if !s.setControl(control) {
		s.enqueueWorker(conn)
		return
	}
	if opts.Logger != nil {
		opts.Logger.Printf("original-compatible control channel established from %s", conn.RemoteAddr())
	}
	go s.monitorControl(control, opts)
}

func (s *originalServerState) enqueueWorker(conn net.Conn) {
	select {
	case s.workers <- conn:
	case <-s.ctx.Done():
		_ = conn.Close()
	default:
		_ = conn.Close()
	}
}

func (s *originalServerState) monitorControl(control *originalControl, opts ServerOpts) {
	signals := make(chan byte, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			signal, err := readOriginalSignal(control.conn)
			if err != nil {
				return
			}
			select {
			case signals <- signal:
			case <-s.ctx.Done():
				return
			}
		}
	}()

	_ = control.writeSignal(originalSignalRTT)
	heartbeat := opts.HeartbeatInterval
	if heartbeat <= 0 {
		heartbeat = 10 * time.Second
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	timeout := opts.HeartbeatTimeout
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	defer s.clearControl(control)
	for {
		select {
		case <-s.ctx.Done():
			_ = control.writeSignal(originalSignalClosed)
			return
		case <-done:
			return
		case <-ticker.C:
			if err := control.writeSignal(originalSignalHeartbeat); err != nil {
				return
			}
		case signal := <-signals:
			switch signal {
			case originalSignalClosed:
				return
			case originalSignalHeartbeat:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case originalSignalRTT:
				// RTT reply is acknowledged by receipt.
			}
		case <-timer.C:
			return
		}
	}
}

func (s *originalServerState) acquireWorker(ctx context.Context, target string, signal byte) (net.Conn, error) {
	var control *originalControl
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for control == nil {
		control = s.currentControl()
		if control != nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	if err := control.writeSignal(originalSignalChannel); err != nil {
		s.clearControl(control)
		return nil, err
	}

	select {
	case worker := <-s.workers:
		if err := writeOriginalTypedString(worker, signal, originalWireTarget(target)); err != nil {
			_ = worker.Close()
			return nil, err
		}
		return worker, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func runOriginalTCPServer(ctx context.Context, opts ServerOpts) error {
	ln, err := net.Listen("tcp", opts.BindAddr)
	if err != nil {
		return err
	}
	defer ln.Close()
	state := newOriginalServerState(ctx, opts.Token, opts.HeartbeatTimeout)
	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		if control := state.currentControl(); control != nil {
			control.close()
		}
	}()
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				if ctx.Err() == nil {
					select {
					case errCh <- acceptErr:
					default:
					}
				}
				return
			}
			go state.handleAccepted(conn, opts)
		}
	}()
	if opts.Logger != nil {
		opts.Logger.Printf("original-compatible TCP control listener on %s", ln.Addr())
	}

	for _, mapping := range opts.Mappings {
		m := mapping
		if err := startPublicListener(ctx, m, opts.Logger, func(reqCtx context.Context, target string) (net.Conn, error) {
			return state.acquireWorker(reqCtx, target, originalSignalTCP)
		}); err != nil {
			return err
		}
		if opts.AcceptUDP {
			if err := startPublicUDPListener(ctx, m, opts.Logger, func(reqCtx context.Context, target string) (net.Conn, error) {
				return state.acquireWorker(reqCtx, target, originalSignalUDP)
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

func runOriginalTCPClient(ctx context.Context, opts ClientOpts) error {
	dialer := net.Dialer{Timeout: opts.DialTimeout}
	var control net.Conn
	for {
		conn, err := dialer.DialContext(ctx, "tcp", opts.RemoteAddr)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(opts.RetryInterval):
				continue
			}
		}
		applyTCPOptions(conn, true, opts.Keepalive)
		_ = conn.SetDeadline(time.Now().Add(opts.HeartbeatTimeout))
		if err := writeOriginalTypedString(conn, originalSignalHello, opts.Token); err != nil {
			_ = conn.Close()
			continue
		}
		token, signal, err := readOriginalTypedString(conn)
		if err != nil || signal != originalSignalChannel || token != opts.Token {
			_ = conn.Close()
			continue
		}
		_ = conn.SetDeadline(time.Time{})
		control = conn
		break
	}
	defer control.Close()
	if opts.Logger != nil {
		opts.Logger.Printf("original-compatible TCP control channel established")
	}

	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	for i := 0; i < opts.PoolSize; i++ {
		go runOriginalWorker(workerCtx, opts)
	}
	go func() {
		<-ctx.Done()
		_ = control.Close()
	}()

	for {
		signal, err := readOriginalSignal(control)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		switch signal {
		case originalSignalChannel:
			go runOriginalWorker(workerCtx, opts)
		case originalSignalRTT:
			err = writeOriginalSignal(control, originalSignalRTT)
			if err != nil {
				return err
			}
		case originalSignalHeartbeat:
			if err := writeOriginalSignal(control, originalSignalHeartbeat); err != nil {
				return err
			}
		case originalSignalClosed:
			return nil
		}
	}
}

func runOriginalWorker(ctx context.Context, opts ClientOpts) {
	dialer := net.Dialer{Timeout: opts.DialTimeout}
	worker, err := dialer.DialContext(ctx, "tcp", opts.RemoteAddr)
	if err != nil {
		return
	}
	defer worker.Close()
	applyTCPOptions(worker, opts.NoDelay, opts.Keepalive)
	go func() {
		<-ctx.Done()
		_ = worker.Close()
	}()

	target, signal, err := readOriginalTypedString(worker)
	if err != nil {
		return
	}
	target, err = normalizeOriginalTarget(target)
	if err != nil {
		return
	}
	switch signal {
	case originalSignalTCP:
		upstream, err := dialer.DialContext(ctx, "tcp", target)
		if err != nil {
			return
		}
		defer upstream.Close()
		applyTCPOptions(upstream, opts.NoDelay, opts.Keepalive)
		bridge(worker, upstream)
	case originalSignalUDP:
		_ = handleUDPClientRelay(worker, target)
	}
}
