// Package server implements the LogService gRPC API: components ship their logs
// here, clients (e.g. the WebUI) list sources and export an archive.
package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	pb "github.com/NurPech/hannah-proto-go/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/export"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
)

// exportChunkSize stays well below gRPC's default 4 MB message limit.
const exportChunkSize = 256 * 1024

type Server struct {
	pb.UnimplementedLogServiceServer

	store   *store.Store
	writer  *store.Writer
	tempDir string // must be writable — the container image has no /tmp
	now     func() time.Time
}

func New(st *store.Store, w *store.Writer, tempDir string) *Server {
	return &Server{store: st, writer: w, tempDir: tempDir, now: time.Now}
}

// Ship receives one component's log stream: a ShipHello first, then entries and gaps.
func (s *Server) Ship(stream grpc.ClientStreamingServer[pb.ShipMessage, pb.ShipAck]) error {
	ctx := stream.Context()

	first, err := stream.Recv()
	if errors.Is(err, io.EOF) {
		return stream.SendAndClose(&pb.ShipAck{})
	}
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil || hello.GetComponent() == "" {
		return status.Error(codes.InvalidArgument, "first message must be a ShipHello with a component")
	}

	sourceID, err := s.store.UpsertSource(ctx, hello.GetComponent(), hello.GetInstance(), hello.GetVersion())
	if err != nil {
		slog.Error("registering source", "component", hello.GetComponent(), "err", err)
		return status.Error(codes.Internal, "registering source failed")
	}
	log := slog.With("component", hello.GetComponent(), "instance", hello.GetInstance())
	log.Info("source connected", "version", hello.GetVersion())

	var accepted int64
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if err := s.writer.Sync(ctx); err != nil {
				return status.Error(codes.Internal, "storing entries failed")
			}
			log.Info("source finished", "accepted", accepted)
			return stream.SendAndClose(&pb.ShipAck{Accepted: accepted})
		}
		if err != nil {
			// Component went away without closing cleanly — what was queued still gets written.
			log.Info("source disconnected", "accepted", accepted, "err", err)
			return err
		}

		switch p := msg.GetPayload().(type) {
		case *pb.ShipMessage_Entry:
			e := p.Entry
			if err := s.writer.Add(ctx, store.Entry{
				SourceID:    sourceID,
				TimestampMs: e.GetTimestampMs(),
				Level:       int32(e.GetLevel()),
				Logger:      e.GetLogger(),
				Message:     e.GetMessage(),
				Category:    int32(e.GetCategory()),
			}); err != nil {
				return err
			}
			accepted++
		case *pb.ShipMessage_Gap:
			g := p.Gap
			if err := s.store.InsertGap(ctx, store.Gap{
				SourceID: sourceID, Dropped: g.GetDropped(), FromMs: g.GetFromMs(), ToMs: g.GetToMs(),
			}); err != nil {
				log.Error("storing gap", "err", err)
			}
		case *pb.ShipMessage_Hello:
			log.Warn("ignoring repeated ShipHello")
		default:
			log.Warn("ignoring unknown ShipMessage payload")
		}
	}
}

func (s *Server) GetSources(ctx context.Context, _ *pb.Empty) (*pb.GetSourcesResponse, error) {
	stats, err := s.store.Sources(ctx)
	if err != nil {
		slog.Error("listing sources", "err", err)
		return nil, status.Error(codes.Internal, "listing sources failed")
	}
	resp := &pb.GetSourcesResponse{}
	for _, st := range stats {
		resp.Sources = append(resp.Sources, &pb.LogSource{
			Component: st.Component,
			Instance:  st.Instance,
			Version:   st.Version,
			OldestMs:  st.OldestMs,
			NewestMs:  st.NewestMs,
			Entries:   st.Entries,
		})
	}
	return resp, nil
}

// Export builds the archive into a temp file first, so a slow client never holds the
// database while it downloads, then streams it in chunks.
func (s *Server) Export(req *pb.ExportRequest, stream grpc.ServerStreamingServer[pb.ExportChunk]) error {
	ctx := stream.Context()

	// Include entries still sitting in the write batch.
	if err := s.writer.Sync(ctx); err != nil {
		return status.Error(codes.Internal, "flushing pending entries failed")
	}

	f := store.Filter{
		SinceMs:    req.GetSinceMs(),
		UntilMs:    req.GetUntilMs(),
		Components: req.GetComponents(),
	}
	for _, c := range req.GetExcludeCategories() {
		f.ExcludeCategories = append(f.ExcludeCategories, int32(c))
	}

	archive, err := os.CreateTemp(s.tempDir, "export-*.tar.gz")
	if err != nil {
		slog.Error("creating export file", "err", err)
		return status.Error(codes.Internal, "creating export failed")
	}
	defer func() {
		archive.Close()
		os.Remove(archive.Name())
	}()

	if err := export.Build(ctx, s.store, f, s.now(), s.tempDir, archive); err != nil {
		slog.Error("building export", "err", err)
		return status.Error(codes.Internal, "building export failed")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return status.Error(codes.Internal, "reading export failed")
	}

	buf := make([]byte, exportChunkSize)
	var sent int64
	for {
		n, err := archive.Read(buf)
		if n > 0 {
			if err := stream.Send(&pb.ExportChunk{Data: buf[:n]}); err != nil {
				return err
			}
			sent += int64(n)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return status.Error(codes.Internal, "reading export failed")
		}
	}
	slog.Info("export sent", "bytes", sent, "components", f.Components, "excluded_categories", f.ExcludeCategories)
	return nil
}
