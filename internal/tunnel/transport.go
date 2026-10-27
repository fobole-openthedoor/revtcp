package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// Commands only exist inside an authenticated TLS connection. After OPEN,
// everything until TLS close_notify is the original application byte stream.
const (
	cmdReady byte = 0
	cmdOpen  byte = 1
	cmdPing  byte = 2
	cmdPong  byte = 3
)

func LoadTLS(certFile, keyFile, caFile string, server bool, serverName string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load certificate: %w", err)
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("CA file contains no certificates")
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	if server {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		cfg.ClientCAs = roots
	} else {
		cfg.RootCAs = roots
		cfg.ServerName = serverName
		cfg.ClientSessionCache = tls.NewLRUClientSessionCache(64)
	}
	return cfg, nil
}

func tuneTCP(conn net.Conn) {
	if c, ok := conn.(*net.TCPConn); ok {
		_ = c.SetNoDelay(true)
		_ = c.SetKeepAlive(true)
		_ = c.SetKeepAlivePeriod(30 * time.Second)
	}
}

// Abort the transport directly: TLS Close may wait to send close_notify.
func abort(conn net.Conn) {
	if c, ok := conn.(*tls.Conn); ok {
		_ = c.NetConn().Close()
	} else {
		_ = conn.Close()
	}
}

func bridge(ctx context.Context, a, b net.Conn) error {
	closeBoth := func() { abort(a); abort(b) }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	defer closeBoth()
	results := make(chan error, 2)
	copyHalf := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if err == nil {
			if cw, ok := dst.(interface{ CloseWrite() error }); ok {
				err = cw.CloseWrite()
			} else {
				err = errors.New("connection does not support half-close")
			}
		}
		if err != nil {
			closeBoth()
		}
		results <- err
	}
	go copyHalf(a, b)
	go copyHalf(b, a)
	return errors.Join(<-results, <-results)
}

func command(conn net.Conn, value byte, timeout time.Duration) error {
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	_, err := conn.Write([]byte{value})
	if err != nil {
		return err
	}
	return conn.SetWriteDeadline(time.Time{})
}
