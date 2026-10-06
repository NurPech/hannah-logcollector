package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	hannahlog "gitlab.com/gessinger/hannah-grpc-lib/go/logging"
	"google.golang.org/grpc"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/config"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/forward"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/hannah"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/server"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/syslog"
)

// version is set at build time via -ldflags.
var version = "dev"

const (
	pruneInterval   = 10 * time.Minute
	shutdownTimeout = 5 * time.Second
)

func main() {
	configPath := flag.String("config", "", "path to YAML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("loading config", "err", err)
		os.Exit(1)
	}

	shipping := setupLogging(cfg.Log.Level)
	slog.Info("starting hannah-logcollector", "version", version, "hannah_addr", cfg.Hannah.Address,
		"listen", cfg.Server.Listen, "db", cfg.DB.Path,
		"retention_days", cfg.Retention.Days, "max_size_mb", cfg.Retention.MaxSizeMB)

	st, err := store.New(cfg.DB.Path)
	if err != nil {
		slog.Error("opening store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	lis, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		slog.Error("listening", "addr", cfg.Server.Listen, "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// The writer outlives the gRPC server, so entries received right before
	// shutdown still get flushed.
	writerCtx, stopWriter := context.WithCancel(context.Background())
	writer := store.NewWriter(st, 500, time.Second)
	writerDone := make(chan struct{})
	go func() { writer.Run(writerCtx); close(writerDone) }()

	// Every entry the writer accepts, from Ship streams and from syslog alike, is also passed on
	// to the syslog receiver, if one is configured. Like the writer it outlives the gRPC
	// server, so the last lines of the components reach the receiver as well.
	forwardCtx, stopForward := context.WithCancel(context.Background())
	forwardDone := make(chan struct{})
	if cfg.Forward.Address == "" {
		close(forwardDone)
	} else {
		fwd := forward.New(forward.Config{Network: cfg.Forward.Protocol, Address: cfg.Forward.Address}, st)
		writer.SetTap(fwd.Offer)
		slog.Info("forwarding entries as syslog", "target", cfg.Forward.Address, "protocol", cfg.Forward.Protocol)
		go func() { fwd.Run(forwardCtx); close(forwardDone) }()
	}

	go runRetention(ctx, st, cfg)

	// The syslog receiver (ESP satellites) feeds the same writer as the Ship streams.
	var syslogPort int32
	syslogDone := make(chan struct{})
	if cfg.Syslog.Listen == "" {
		close(syslogDone)
	} else {
		recv, err := syslog.Listen(cfg.Syslog.Listen, st, writer)
		if err != nil {
			slog.Error("listening for syslog", "addr", cfg.Syslog.Listen, "err", err)
			os.Exit(1)
		}
		if udp, ok := recv.Addr().(*net.UDPAddr); ok {
			syslogPort = int32(udp.Port)
		}
		slog.Info("syslog receiver listening", "addr", recv.Addr().String())
		go func() { recv.Run(ctx); close(syslogDone) }()
	}

	// Temp files for exports live next to the database — the container has no /tmp.
	srv := grpc.NewServer()
	server.Register(srv, server.New(st, writer, filepath.Dir(cfg.DB.Path)))
	go func() {
		if err := srv.Serve(lis); err != nil {
			slog.Error("gRPC server stopped", "err", err)
			cancel()
		}
	}()

	// The collector ships its own logs like every other component, into itself: the Hannah
	// address enables the heartbeat and discovery, its own address is the fallback, so the logs
	// also arrive while Hannah Core is down. Only now the server listens, until then the buffer
	// of the library holds what was logged.
	shipping.Connect(cfg.Hannah.Address, selfAddress(lis.Addr()))

	client := hannah.New(cfg.Hannah.Address, hannah.Registration{
		Instance: cfg.Server.Instance,
		Host:     cfg.Server.AdvertiseHost,
		Port:     int32(cfg.EffectiveAdvertisePort()),
		Version:  version,

		SyslogPort: syslogPort,
	})
	go client.Run(ctx)

	<-ctx.Done()
	slog.Info("shutting down")
	// First, while the server still takes Ship streams: what is buffered goes into the store.
	shipping.Close(shutdownTimeout)
	stopServer(srv)
	<-syslogDone
	stopWriter()
	<-writerDone
	stopForward() // the writer is done, nothing new comes in: send what is left and stop
	<-forwardDone
	slog.Info("shutdown complete")
}

// stopServer waits briefly for in-flight calls; Ship streams stay open for as long
// as their component runs, so a graceful stop alone would never return.
func stopServer(srv *grpc.Server) {
	stopped := make(chan struct{})
	go func() { srv.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(shutdownTimeout):
		srv.Stop()
	}
}

func runRetention(ctx context.Context, st *store.Store, cfg *config.Config) {
	maxAge := time.Duration(cfg.Retention.Days) * 24 * time.Hour
	maxBytes := int64(cfg.Retention.MaxSizeMB) << 20

	prune := func() {
		res, err := st.Prune(ctx, time.Now(), maxAge, maxBytes)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("pruning logs", "err", err)
			}
			return
		}
		if res.ByAge > 0 || res.BySize > 0 {
			slog.Info("pruned logs", "by_age", res.ByAge, "by_size", res.BySize)
		}
	}

	prune()
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

// selfAddress is where the collector's own gRPC server can be reached from this host: the
// listener's address, with loopback for an unspecified one (":50060").
func selfAddress(addr net.Addr) string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return addr.String()
	}
	host := tcp.IP
	if len(host) == 0 || host.IsUnspecified() {
		host = net.IPv4(127, 0, 0, 1)
	}
	return net.JoinHostPort(host.String(), strconv.Itoa(tcp.Port))
}

// setupLogging writes JSON to stdout, as before, and also buffers every record for
// shipping (hannah-grpc-lib). The shipping names the component and its version in every
// call to Hannah Core. Connect starts it once the addresses are known.
func setupLogging(level string) *hannahlog.Shipping {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	shipping, err := hannahlog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}), hannahlog.Options{
		Component: "logcollector",
		Version:   strings.TrimPrefix(version, "v"),
	})
	if err != nil {
		slog.Error("setting up log shipping", "err", err)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(shipping.Handler()))
	return shipping
}
