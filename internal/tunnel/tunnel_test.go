package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"revtcp/internal/pki"
)

type readyHandler struct{ ready chan struct{} }

func (h readyHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h readyHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "reverse connection ready" {
		select {
		case h.ready <- struct{}{}:
		default:
		}
	}
	return nil
}
func (h readyHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h readyHandler) WithGroup(string) slog.Handler      { return h }

func testTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "certs")
	if err := pki.Generate(dir, []string{"localhost", "127.0.0.1"}, 1); err != nil {
		t.Fatal(err)
	}
	load := func(name string, server bool) *tls.Config {
		cfg, err := LoadTLS(filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key"), filepath.Join(dir, "ca.crt"), server, "localhost")
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	return load("server", true), load("client", false)
}

func listen(t *testing.T, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func stopRunner(t *testing.T, cancel context.CancelFunc, done <-chan error) func() {
	t.Helper()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("runner did not stop on cancellation")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func startServer(t *testing.T, cfgTLS *tls.Config, workerAddr string, change func(*ServerConfig)) (string, string, func()) {
	t.Helper()
	workers := listen(t, workerAddr)
	public := listen(t, "127.0.0.1:0")
	cfg := DefaultServerConfig()
	cfg.TLS = cfgTLS
	cfg.Heartbeat = 20 * time.Millisecond
	cfg.HeartbeatTimeout = time.Second
	cfg.HandshakeTimeout = time.Second
	cfg.WaitTimeout = time.Second
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	if change != nil {
		change(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, workers, public, cfg) }()
	return workers.Addr().String(), public.Addr().String(), stopRunner(t, cancel, done)
}

func startClient(t *testing.T, cfgTLS *tls.Config, server, target string, change func(*ClientConfig)) (<-chan struct{}, func()) {
	t.Helper()
	cfg := DefaultClientConfig()
	cfg.TLS, cfg.Server, cfg.Target = cfgTLS, server, target
	cfg.Pool, cfg.MaxConnections = 2, 64
	cfg.RetryMin, cfg.RetryMax = 5*time.Millisecond, 50*time.Millisecond
	cfg.DialTimeout, cfg.IdleTimeout = time.Second, time.Second
	ready := make(chan struct{}, 256)
	cfg.Logger = slog.New(readyHandler{ready})
	if change != nil {
		change(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunClient(ctx, cfg) }()
	return ready, stopRunner(t, cancel, done)
}

func backend(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln := listen(t, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		defer wg.Wait()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				handle(conn)
			}()
		}
	}()
	t.Cleanup(func() { cancel(); _ = ln.Close(); await(t, done) })
	return ln.Addr().String()
}

func dial(t *testing.T, addr string) *net.TCPConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn.(*net.TCPConn)
}

func roundtrip(addr string, payload []byte) error {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	written := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, bytes.NewReader(payload))
		if err == nil {
			err = conn.(*net.TCPConn).CloseWrite()
		}
		written <- err
	}()
	got, err := io.ReadAll(conn)
	if err != nil {
		_ = conn.Close()
	}
	writeErr := <-written
	if err != nil {
		return err
	}
	if writeErr != nil {
		return writeErr
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("data mismatch: got %d bytes, want %d", len(got), len(payload))
	}
	return nil
}

func TestBinaryConcurrentForwardingAndPoolRefill(t *testing.T) {
	srvTLS, cliTLS := testTLS(t)
	target := backend(t, func(conn net.Conn) { _, _ = io.Copy(conn, conn) })
	server, public, _ := startServer(t, srvTLS, "127.0.0.1:0", nil)
	ready, _ := startClient(t, cliTLS, server, target, nil)
	await(t, ready)
	await(t, ready)
	payload := make([]byte, 512*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 24)
	for range 24 { // Concurrency substantially exceeds the initial pool of two.
		go func() { errs <- roundtrip(public, payload) }()
	}
	for range 24 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	await(t, ready)
}

func TestRequestHalfClosePreservesDelayedResponse(t *testing.T) {
	srvTLS, cliTLS := testTLS(t)
	target := backend(t, func(conn net.Conn) {
		data, err := io.ReadAll(conn) // Must receive EOF before producing a reply.
		if err == nil {
			_, _ = conn.Write(append([]byte("reply:"), data...))
		}
	})
	server, public, _ := startServer(t, srvTLS, "127.0.0.1:0", nil)
	ready, _ := startClient(t, cliTLS, server, target, nil)
	await(t, ready)
	conn := dial(t, public)
	_, _ = conn.Write([]byte{0, 1, 2, 3, 255})
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(conn)
	if err != nil || !bytes.Equal(got, []byte{'r', 'e', 'p', 'l', 'y', ':', 0, 1, 2, 3, 255}) {
		t.Fatalf("response %q, error %v", got, err)
	}
}

func TestServerFirstAndReverseHalfClose(t *testing.T) {
	srvTLS, cliTLS := testTLS(t)
	received := make(chan []byte, 1)
	target := backend(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("hello"))
		_ = conn.(*net.TCPConn).CloseWrite()
		data, _ := io.ReadAll(conn)
		received <- data
	})
	server, public, _ := startServer(t, srvTLS, "127.0.0.1:0", nil)
	ready, _ := startClient(t, cliTLS, server, target, nil)
	await(t, ready)
	conn := dial(t, public)
	got, err := io.ReadAll(conn)
	if err != nil || string(got) != "hello" {
		t.Fatalf("greeting %q, error %v", got, err)
	}
	_, _ = conn.Write([]byte("still writable"))
	_ = conn.CloseWrite()
	select {
	case got := <-received:
		if string(got) != "still writable" {
			t.Fatalf("reverse half-close lost data: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not receive response")
	}
}

func TestReconnectAfterServerRestart(t *testing.T) {
	srvTLS, cliTLS := testTLS(t)
	target := backend(t, func(conn net.Conn) { _, _ = io.Copy(conn, conn) })
	server, public, stop := startServer(t, srvTLS, "127.0.0.1:0", nil)
	ready, _ := startClient(t, cliTLS, server, target, func(cfg *ClientConfig) { cfg.Pool = 1 })
	await(t, ready)
	if err := roundtrip(public, []byte("before restart")); err != nil {
		t.Fatal(err)
	}
	await(t, ready) // Replacement must be ready before stopping the server.
	stop()
	_, public, _ = startServer(t, srvTLS, server, nil)
	await(t, ready)
	if err := roundtrip(public, []byte("after restart")); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatKeepsIdleWorkerUsable(t *testing.T) {
	srvTLS, cliTLS := testTLS(t)
	target := backend(t, func(conn net.Conn) { _, _ = io.Copy(conn, conn) })
	server, public, _ := startServer(t, srvTLS, "127.0.0.1:0", nil)
	ready, _ := startClient(t, cliTLS, server, target, func(cfg *ClientConfig) {
		cfg.Pool = 1
		cfg.IdleTimeout = 250 * time.Millisecond
	})
	await(t, ready)
	select {
	case <-ready:
		t.Fatal("idle worker was unnecessarily replaced")
	case <-time.After(750 * time.Millisecond): // Three idle read timeouts.
	}
	if err := roundtrip(public, []byte("after idle")); err != nil {
		t.Fatal(err)
	}
}

func TestTLSRejectsWrongIdentityAndServerName(t *testing.T) {
	srvTLS, cliTLS := testTLS(t)
	_, foreignTLS := testTLS(t)
	server, _, _ := startServer(t, srvTLS, "127.0.0.1:0", nil)
	cases := map[string]*tls.Config{}
	missing := cliTLS.Clone()
	missing.Certificates = nil
	cases["missing client certificate"] = missing
	foreign := cliTLS.Clone()
	foreign.Certificates = foreignTLS.Certificates
	cases["untrusted client certificate"] = foreign
	wrongName := cliTLS.Clone()
	wrongName.ServerName = "wrong.example"
	cases["wrong server name"] = wrongName
	wrongCA := cliTLS.Clone()
	wrongCA.RootCAs = foreignTLS.RootCAs
	cases["untrusted server certificate"] = wrongCA
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", server, cfg)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			var ready [1]byte
			if _, err := io.ReadFull(conn, ready[:]); err == nil {
				t.Fatal("unauthorized connection was admitted")
			}
		})
	}
}

func TestNoWorkerWaitIsBounded(t *testing.T) {
	srvTLS, _ := testTLS(t)
	_, public, _ := startServer(t, srvTLS, "127.0.0.1:0", func(cfg *ServerConfig) { cfg.WaitTimeout = 50 * time.Millisecond })
	conn := dial(t, public)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	_, err := conn.Read(b[:])
	if err == nil {
		t.Fatal("expected connection closure")
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("visitor was not closed after wait timeout")
	}
}

func TestMaxConnectionsAndActiveCancellation(t *testing.T) {
	srvTLS, cliTLS := testTLS(t)
	target := backend(t, func(conn net.Conn) { _, _ = io.Copy(conn, conn) })
	server, public, _ := startServer(t, srvTLS, "127.0.0.1:0", func(cfg *ServerConfig) { cfg.WaitTimeout = 100 * time.Millisecond })
	ready, stopClient := startClient(t, cliTLS, server, target, func(cfg *ClientConfig) {
		cfg.Pool, cfg.MaxConnections = 1, 1
	})
	await(t, ready)
	first := dial(t, public)
	_, _ = first.Write([]byte("a"))
	var b [1]byte
	if _, err := io.ReadFull(first, b[:]); err != nil || b[0] != 'a' {
		t.Fatalf("first connection: %v", err)
	}
	second := dial(t, public)
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := second.Read(b[:]); err == nil {
		t.Fatal("limit exceeded")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("unassigned visitor was not closed")
	}
	stopClient() // Must stop while a business stream remains open.
	_ = first.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := first.Read(b[:]); err == nil {
		t.Fatal("active connection survived cancellation")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("active connection hung after cancellation")
	}
}

func TestOpenForwardsPayloadWithoutAcknowledgement(t *testing.T) {
	srvTLS, cliTLS := testTLS(t)
	server, public, _ := startServer(t, srvTLS, "127.0.0.1:0", func(cfg *ServerConfig) {
		cfg.Heartbeat = time.Hour // Isolate OPEN from heartbeat traffic.
	})
	worker, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", server, cliTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	_ = worker.SetDeadline(time.Now().Add(time.Second))
	var cmd [1]byte
	if _, err := io.ReadFull(worker, cmd[:]); err != nil || cmd[0] != cmdReady {
		t.Fatalf("READY: %v, %v", cmd, err)
	}
	visitor := dial(t, public)
	_, _ = visitor.Write([]byte("first flight"))
	// A minimal authenticated worker deliberately never acknowledges OPEN.
	got := make([]byte, 1+len("first flight"))
	if _, err := io.ReadFull(worker, got); err != nil {
		t.Fatal(err)
	}
	if got[0] != cmdOpen || string(got[1:]) != "first flight" {
		t.Fatalf("unexpected first flight %q", got)
	}
}

func TestPoolReplenishesWhileStreamsRemainActive(t *testing.T) {
	srvTLS, cliTLS := testTLS(t)
	target := backend(t, func(conn net.Conn) { _, _ = io.Copy(conn, conn) })
	server, public, _ := startServer(t, srvTLS, "127.0.0.1:0", nil)
	ready, _ := startClient(t, cliTLS, server, target, func(cfg *ClientConfig) { cfg.Pool = 1 })
	await(t, ready)
	var active []*net.TCPConn
	for range 5 {
		conn := dial(t, public)
		active = append(active, conn)
		_, _ = conn.Write([]byte{42})
		var b [1]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil || b[0] != 42 {
			t.Fatalf("forwarding: %v", err)
		}
		await(t, ready) // A spare is created before this business stream closes.
	}
	for _, conn := range active {
		_, _ = conn.Write([]byte{43})
		var b [1]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil || b[0] != 43 {
			t.Fatalf("existing stream was interrupted: %v", err)
		}
	}
}

func TestTargetFailureClosesVisitorAndReplenishesPool(t *testing.T) {
	srvTLS, cliTLS := testTLS(t)
	unused := listen(t, "127.0.0.1:0")
	target := unused.Addr().String()
	_ = unused.Close()
	server, public, _ := startServer(t, srvTLS, "127.0.0.1:0", nil)
	ready, _ := startClient(t, cliTLS, server, target, func(cfg *ClientConfig) { cfg.Pool = 1 })
	await(t, ready)
	conn := dial(t, public)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [1]byte
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("unreachable target accepted a stream")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("target failure did not close visitor")
	}
	await(t, ready)
}
