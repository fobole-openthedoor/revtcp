package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"revtcp/internal/pki"
	"revtcp/internal/rotatelog"
	"revtcp/internal/tunnel"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "revtcp:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintf(os.Stdout, "revtcp %s commit=%s built=%s %s/%s %s\n", version, commit, buildTime, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return nil
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stdout, `revtcp — raw TCP reverse tunnel over mutually authenticated TLS

Usage:
  revtcp init   --hosts SERVER_IP_OR_DNS [--out certs]
  revtcp server --listen :7443 --expose :6000
  revtcp client --server SERVER:7443 --target 127.0.0.1:8080
  revtcp client --config /path/to/client.json [--check-config]
  revtcp version

Use "revtcp <command> --help" for all flags.`)
		return nil
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	if args[0] == "init" {
		hosts := fs.String("hosts", "localhost,127.0.0.1,::1", "comma-separated server DNS names/IPs, without ports")
		out := fs.String("out", "certs", "new certificate directory (must not exist)")
		days := fs.Int("days", 365, "certificate lifetime in days")
		if err := parse(fs, args[1:]); err != nil {
			return err
		}
		if err := pki.Generate(*out, strings.Split(*hosts, ","), *days); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Certificates written to", *out)
		return nil
	}
	if args[0] != "server" && args[0] != "client" {
		return fmt.Errorf("unknown command %q", args[0])
	}
	cert := fs.String("cert", "certs/"+args[0]+".crt", "identity certificate PEM file")
	key := fs.String("key", "certs/"+args[0]+".key", "identity private key PEM file")
	ca := fs.String("ca", "certs/ca.crt", "trusted CA PEM file")
	debug := fs.Bool("debug", false, "log individual connections and forwarding errors")
	logFile := fs.String("log-file", "", "rotating log path; empty writes to stderr")
	logMaxMB := fs.Int("log-max-mb", 10, "rotate log after this many MiB")
	logBackups := fs.Int("log-backups", 3, "number of rotated log files to retain (1..100)")
	logger := func() (*slog.Logger, func(), error) {
		level := slog.LevelInfo
		if *debug {
			level = slog.LevelDebug
		}
		var output io.Writer = os.Stderr
		closeLog := func() {}
		if *logFile != "" {
			if *logMaxMB < 1 || *logMaxMB > 1024 {
				return nil, nil, errors.New("log-max-mb must be 1..1024")
			}
			w, err := rotatelog.Open(*logFile, int64(*logMaxMB)*1024*1024, *logBackups)
			if err != nil {
				return nil, nil, err
			}
			output, closeLog = w, func() { _ = w.Close() }
		}
		return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: level})), closeLog, nil
	}
	if args[0] == "server" {
		cfg := tunnel.DefaultServerConfig()
		listen := fs.String("listen", ":7443", "reverse tunnel TCP listen address")
		expose := fs.String("expose", "127.0.0.1:6000", "forwarded public TCP listen address")
		fs.IntVar(&cfg.MaxConnections, "max-conns", cfg.MaxConnections, "connection limit per listener, including idle/pending connections")
		fs.DurationVar(&cfg.HandshakeTimeout, "handshake-timeout", cfg.HandshakeTimeout, "worker TLS handshake timeout")
		fs.DurationVar(&cfg.WaitTimeout, "wait-timeout", cfg.WaitTimeout, "maximum visitor wait for an idle worker")
		fs.DurationVar(&cfg.Heartbeat, "heartbeat", cfg.Heartbeat, "idle-worker heartbeat interval")
		fs.DurationVar(&cfg.HeartbeatTimeout, "heartbeat-timeout", cfg.HeartbeatTimeout, "heartbeat and control write timeout")
		if err := parse(fs, args[1:]); err != nil {
			return err
		}
		var err error
		cfg.TLS, err = tunnel.LoadTLS(*cert, *key, *ca, true, "")
		if err != nil {
			return err
		}
		var closeLog func()
		cfg.Logger, closeLog, err = logger()
		if err != nil {
			return err
		}
		defer closeLog()
		workers, err := net.Listen("tcp", *listen)
		if err != nil {
			return fmt.Errorf("listen tunnel: %w", err)
		}
		defer workers.Close()
		public, err := net.Listen("tcp", *expose)
		if err != nil {
			return fmt.Errorf("listen public: %w", err)
		}
		defer public.Close()
		return tunnel.Serve(ctx, workers, public, cfg)
	}
	cfg := tunnel.DefaultClientConfig()
	configFile := fs.String("config", "", "client JSON config; explicit CLI flags override file values")
	checkConfig := fs.Bool("check-config", false, "validate configuration and local certificate/log files, then exit without connecting")
	fs.StringVar(&cfg.Server, "server", "", "server tunnel host:port (required)")
	fs.StringVar(&cfg.Target, "target", "", "local target host:port (required)")
	serverName := fs.String("server-name", "", "TLS certificate name override (defaults to server host)")
	fs.IntVar(&cfg.Pool, "pool", cfg.Pool, "number of spare reverse connections")
	fs.IntVar(&cfg.MaxConnections, "max-conns", cfg.MaxConnections, "maximum spare + dialing + active connections; must be >= pool")
	fs.DurationVar(&cfg.DialTimeout, "dial-timeout", cfg.DialTimeout, "timeout for each dial, TLS handshake and control write")
	fs.DurationVar(&cfg.IdleTimeout, "idle-timeout", cfg.IdleTimeout, "idle control read timeout; must exceed server heartbeat interval + timeout")
	fs.DurationVar(&cfg.RetryMin, "retry-min", cfg.RetryMin, "initial reconnect backoff (with jitter)")
	fs.DurationVar(&cfg.RetryMax, "retry-max", cfg.RetryMax, "maximum reconnect backoff")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	if *configFile != "" {
		if err := applyClientFile(fs, *configFile); err != nil {
			return err
		}
	}
	var err error
	cfg.TLS, err = tunnel.LoadTLS(*cert, *key, *ca, false, *serverName)
	if err != nil {
		return err
	}
	if err := tunnel.ValidateClientConfig(cfg); err != nil {
		return err
	}
	var closeLog func()
	cfg.Logger, closeLog, err = logger()
	if err != nil {
		return err
	}
	defer closeLog()
	if *checkConfig {
		fmt.Fprintln(os.Stdout, "Client configuration, certificate files and log path checked; network was not tested.")
		return nil
	}
	return tunnel.RunClient(ctx, cfg)
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}
	return nil
}
