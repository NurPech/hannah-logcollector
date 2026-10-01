package server

import (
	"context"

	v1pb "github.com/NurPech/hannah-proto-go/v5/hannahv1"
	pb "github.com/NurPech/hannah-proto-go/v5/hannahv2"
	"google.golang.org/grpc"
)

// v1Server serves hannah.v1.LogService (N−1) for components on an older logging library.
// The generations aren't assumed to be wire-identical: it converts every message to its
// hannah.v2 counterpart and leaves the work to the v2 handlers.
type v1Server struct {
	v1pb.UnimplementedLogServiceServer
	srv *Server
}

func (v v1Server) Ship(stream grpc.ClientStreamingServer[v1pb.ShipMessage, v1pb.ShipAck]) error {
	return v.srv.Ship(v1ShipStream{stream})
}

func (v v1Server) GetSources(ctx context.Context, _ *v1pb.Empty) (*v1pb.GetSourcesResponse, error) {
	resp, err := v.srv.GetSources(ctx, &pb.Empty{})
	if err != nil {
		return nil, err
	}
	out := &v1pb.GetSourcesResponse{}
	for _, s := range resp.GetSources() {
		out.Sources = append(out.Sources, &v1pb.LogSource{
			Component: s.GetComponent(),
			Instance:  s.GetInstance(),
			Version:   s.GetVersion(),
			OldestMs:  s.GetOldestMs(),
			NewestMs:  s.GetNewestMs(),
			Entries:   s.GetEntries(),
		})
	}
	return out, nil
}

func (v v1Server) Export(req *v1pb.ExportRequest, stream grpc.ServerStreamingServer[v1pb.ExportChunk]) error {
	out := &pb.ExportRequest{
		SinceMs:    req.GetSinceMs(),
		UntilMs:    req.GetUntilMs(),
		Components: req.GetComponents(),
	}
	for _, c := range req.GetExcludeCategories() {
		out.ExcludeCategories = append(out.ExcludeCategories, pb.LogCategory(pb.LogCategory_value[c.String()]))
	}
	return v.srv.Export(out, v1ExportStream{stream})
}

// v1ShipStream presents a hannah.v1 Ship stream as a hannah.v2 one.
type v1ShipStream struct {
	grpc.ClientStreamingServer[v1pb.ShipMessage, v1pb.ShipAck]
}

func (s v1ShipStream) Recv() (*pb.ShipMessage, error) {
	msg, err := s.ClientStreamingServer.Recv()
	if err != nil {
		return nil, err
	}
	out := &pb.ShipMessage{}
	switch p := msg.GetPayload().(type) {
	case *v1pb.ShipMessage_Hello:
		out.Payload = &pb.ShipMessage_Hello{Hello: &pb.ShipHello{
			Component: p.Hello.GetComponent(), Instance: p.Hello.GetInstance(), Version: p.Hello.GetVersion(),
		}}
	case *v1pb.ShipMessage_Entry:
		out.Payload = &pb.ShipMessage_Entry{Entry: &pb.LogEntry{
			TimestampMs: p.Entry.GetTimestampMs(),
			Level:       pb.LogLevel(pb.LogLevel_value[p.Entry.GetLevel().String()]),
			Logger:      p.Entry.GetLogger(),
			Message:     p.Entry.GetMessage(),
			Category:    pb.LogCategory(pb.LogCategory_value[p.Entry.GetCategory().String()]),
		}}
	case *v1pb.ShipMessage_Gap:
		out.Payload = &pb.ShipMessage_Gap{Gap: &pb.LogGap{
			Dropped: p.Gap.GetDropped(), FromMs: p.Gap.GetFromMs(), ToMs: p.Gap.GetToMs(),
		}}
	}
	return out, nil
}

func (s v1ShipStream) SendAndClose(ack *pb.ShipAck) error {
	return s.ClientStreamingServer.SendAndClose(&v1pb.ShipAck{Accepted: ack.GetAccepted()})
}

// v1ExportStream presents a hannah.v1 Export stream as a hannah.v2 one.
type v1ExportStream struct {
	grpc.ServerStreamingServer[v1pb.ExportChunk]
}

func (s v1ExportStream) Send(chunk *pb.ExportChunk) error {
	return s.ServerStreamingServer.Send(&v1pb.ExportChunk{Data: chunk.GetData()})
}
