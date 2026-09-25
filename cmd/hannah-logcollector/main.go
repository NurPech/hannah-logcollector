package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	pb "github.com/NurPech/hannah-proto-go/v4"
	"google.golang.org/grpc"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/config"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/hannah"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/server"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
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

	setupLogging(cfg.Log.Level)
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

	go runRetention(ctx, st, cfg)

	// Temp files for exports live next to the database — the container has no /tmp.
	srv := grpc.NewServer()
	pb.RegisterLogServiceServer(srv, server.New(st, writer, filepath.Dir(cfg.DB.Path)))
	go func() {
		if err := srv.Serve(lis); err != nil {
			slog.Error("gRPC server stopped", "err", err)
			cancel()
		}
	}()

	client := hannah.New(cfg.Hannah.Address, hannah.Registration{
		Instance: cfg.Server.Instance,
		Host:     cfg.Server.AdvertiseHost,
		Port:     int32(cfg.EffectiveAdvertisePort()),
		Version:  version,
	})
	go client.Run(ctx)

	<-ctx.Done()
	slog.Info("shutting down")
	stopServer(srv)
	stopWriter()
	<-writerDone
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

func setupLogging(level string) {
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}
