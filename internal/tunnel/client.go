package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"strconv"
	"sync"
	"time"
)

type ClientConfig struct {
	TLS            *tls.Config
	Server         string
	Target         string
	Pool           int
	MaxConnections int
	DialTimeout    time.Duration
	IdleTimeout    time.Duration
	RetryMin       time.Duration
	RetryMax       time.Duration
	Logger         *slog.Logger
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		Pool: 16, MaxConnections: 1024, DialTimeout: 10 * time.Second,
		IdleTimeout: 90 * time.Second, RetryMin: 250 * time.Millisecond,
		RetryMax: 15 * time.Second, Logger: slog.Default(),
	}
}

// ValidateClientConfig checks local settings without making network connections.
func ValidateClientConfig(cfg ClientConfig) error {
	if cfg.TLS == nil || cfg.TLS.InsecureSkipVerify || len(cfg.TLS.Certificates) == 0 {
		return errors.New("client requires verified mutual TLS")
	}
	if cfg.Pool < 1 || cfg.MaxConnections < cfg.Pool || cfg.DialTimeout <= 0 || cfg.IdleTimeout <= 0 || cfg.RetryMin <= 0 || cfg.RetryMax < cfg.RetryMin {
		return errors.New("invalid client limits or timeouts (max-conns must be >= pool)")
	}
	for _, addr := range []string{cfg.Server, cfg.Target} {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("invalid address %q: %w", addr, err)
		}
		n, err := strconv.Atoi(port)
		if host == "" || err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid address %q: require a hostname/IP and numeric port 1..65535", addr)
		}
	}
	return nil
}

// RunClient maintains Pool spare connections, replenishing each immediately on
// OPEN, while MaxConnections bounds spare + dialing + active connections.
func RunClient(ctx context.Context, cfg ClientConfig) error {
	if err := ValidateClientConfig(cfg); err != nil {
		return err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	// Clone before deriving ServerName so callers can share the original config.
	cfg.TLS = cfg.TLS.Clone()
	if cfg.TLS.ServerName == "" {
		cfg.TLS.ServerName, _, _ = net.SplitHostPort(cfg.Server)
	}
	idle := make(chan struct{}, cfg.Pool)
	total := make(chan struct{}, cfg.MaxConnections)
	var sessions sync.WaitGroup
	defer sessions.Wait()
	cfg.Logger.Info("client connecting", "server", cfg.Server, "target", cfg.Target, "pool", cfg.Pool)
	for {
		select {
		case idle <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		select {
		case total <- struct{}{}:
		case <-ctx.Done():
			<-idle
			return nil
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			defer func() { <-total }()
			var release sync.Once
			releaseIdle := func() { release.Do(func() { <-idle }) }
			defer releaseIdle()
			backoff := cfg.RetryMin
			for ctx.Err() == nil {
				started := time.Now()
				assigned, err := clientSession(ctx, cfg, releaseIdle)
				if assigned || ctx.Err() != nil {
					if err != nil && ctx.Err() == nil {
						cfg.Logger.Debug("forwarding ended", "error", err)
					}
					return
				}
				if time.Since(started) >= cfg.IdleTimeout {
					backoff = cfg.RetryMin
				}
				delay := backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1))
				cfg.Logger.Warn("reverse connection lost; retrying", "error", err, "retry_in", delay)
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return
				}
				if backoff < cfg.RetryMax/2 {
					backoff *= 2
				} else {
					backoff = cfg.RetryMax
				}
			}
		}()
	}
}

func clientSession(ctx context.Context, cfg ClientConfig, onOpen func()) (bool, error) {
	dialer := net.Dialer{Timeout: cfg.DialTimeout, KeepAlive: 30 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", cfg.Server)
	if err != nil {
		return false, err
	}
	defer raw.Close()
	tuneTCP(raw)
	stop := context.AfterFunc(ctx, func() { abort(raw) })
	defer stop()
	conn := tls.Client(raw, cfg.TLS)
	handshakeCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	err = conn.HandshakeContext(handshakeCtx)
	cancel()
	if err != nil {
		return false, err
	}
	ready := false
	for {
		if err := conn.SetReadDeadline(time.Now().Add(cfg.IdleTimeout)); err != nil {
			return false, err
		}
		var cmd [1]byte
		if _, err := io.ReadFull(conn, cmd[:]); err != nil {
			return false, err
		}
		if !ready {
			if cmd[0] != cmdReady {
				return false, errors.New("expected READY")
			}
			ready = true
			cfg.Logger.Debug("reverse connection ready")
			continue
		}
		switch cmd[0] {
		case cmdPing:
			if err := command(conn, cmdPong, cfg.DialTimeout); err != nil {
				return false, err
			}
		case cmdOpen:
			onOpen()
			if err := conn.SetDeadline(time.Time{}); err != nil {
				return true, err
			}
			target, err := dialer.DialContext(ctx, "tcp", cfg.Target)
			if err != nil {
				return true, fmt.Errorf("dial target: %w", err)
			}
			tuneTCP(target)
			return true, bridge(ctx, conn, target)
		default:
			return false, fmt.Errorf("unknown command: %d", cmd[0])
		}
	}
}
