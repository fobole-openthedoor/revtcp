package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

type ServerConfig struct {
	TLS              *tls.Config
	MaxConnections   int
	HandshakeTimeout time.Duration
	WaitTimeout      time.Duration
	Heartbeat        time.Duration
	HeartbeatTimeout time.Duration
	Logger           *slog.Logger
}

func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		MaxConnections: 4096, HandshakeTimeout: 10 * time.Second,
		WaitTimeout: 10 * time.Second, Heartbeat: 20 * time.Second,
		HeartbeatTimeout: 5 * time.Second, Logger: slog.Default(),
	}
}

type visitor struct {
	conn net.Conn
	done chan struct{}
}

// Serve owns both listeners after successful validation, closing them and all
// live sessions on cancellation. Each authenticated worker serves one visitor.
func Serve(ctx context.Context, workers, public net.Listener, cfg ServerConfig) error {
	if cfg.TLS == nil || cfg.TLS.ClientAuth != tls.RequireAndVerifyClientCert {
		return errors.New("server requires mutual TLS")
	}
	if cfg.MaxConnections < 1 || cfg.HandshakeTimeout <= 0 || cfg.WaitTimeout <= 0 || cfg.Heartbeat <= 0 || cfg.HeartbeatTimeout <= 0 {
		return errors.New("server limits and timeouts must be positive")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = workers.Close(); _ = public.Close() })
	defer stop()
	defer workers.Close()
	defer public.Close()
	jobs := make(chan visitor)
	var sessions sync.WaitGroup
	errs := make(chan error, 2)
	accept := func(ln net.Listener, handle func(net.Conn)) {
		limit := make(chan struct{}, cfg.MaxConnections)
		for {
			conn, err := ln.Accept()
			if err != nil {
				errs <- err
				return
			}
			tuneTCP(conn)
			select {
			case limit <- struct{}{}:
				sessions.Add(1)
				go func() {
					defer sessions.Done()
					defer func() { <-limit }()
					defer conn.Close()
					handle(conn)
				}()
			default:
				_ = conn.Close()
				cfg.Logger.Debug("connection limit reached", "listener", ln.Addr())
			}
		}
	}
	go accept(public, func(conn net.Conn) {
		job := visitor{conn: conn, done: make(chan struct{})}
		timer := time.NewTimer(cfg.WaitTimeout)
		defer timer.Stop()
		select {
		case jobs <- job:
			select {
			case <-job.done:
			case <-ctx.Done():
			}
		case <-timer.C:
			cfg.Logger.Debug("no available reverse connection", "peer", conn.RemoteAddr())
		case <-ctx.Done():
		}
	})
	go accept(workers, func(raw net.Conn) {
		stop := context.AfterFunc(ctx, func() { abort(raw) })
		defer stop()
		conn := tls.Server(raw, cfg.TLS)
		handshakeCtx, cancel := context.WithTimeout(ctx, cfg.HandshakeTimeout)
		err := conn.HandshakeContext(handshakeCtx)
		cancel()
		if err != nil {
			cfg.Logger.Debug("worker handshake failed", "peer", raw.RemoteAddr(), "error", err)
			return
		}
		if err := command(conn, cmdReady, cfg.HeartbeatTimeout); err != nil {
			return
		}
		cfg.Logger.Debug("reverse connection ready", "peer", raw.RemoteAddr())
		ticker := time.NewTicker(cfg.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case job := <-jobs:
				defer close(job.done)
				if err := command(conn, cmdOpen, cfg.HeartbeatTimeout); err != nil {
					return
				}
				// No OPEN acknowledgement: payload can be written immediately.
				if err := bridge(ctx, conn, job.conn); err != nil && ctx.Err() == nil {
					cfg.Logger.Debug("forwarding ended", "error", err)
				}
				return
			case <-ticker.C:
				if err := ping(conn, cfg.HeartbeatTimeout); err != nil {
					cfg.Logger.Debug("idle worker lost", "error", err)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	})
	cfg.Logger.Info("server listening", "tunnel", workers.Addr(), "public", public.Addr())
	err := <-errs
	wasCanceled := ctx.Err() != nil
	cancel()
	_ = workers.Close()
	_ = public.Close()
	<-errs // Both accept loops must stop before Wait: no more Add calls.
	sessions.Wait()
	if wasCanceled {
		return nil
	}
	return fmt.Errorf("accept: %w", err)
}

func ping(conn net.Conn, timeout time.Duration) error {
	if err := command(conn, cmdPing, timeout); err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	var reply [1]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply[0] != cmdPong {
		return errors.New("invalid heartbeat reply")
	}
	return conn.SetReadDeadline(time.Time{})
}
