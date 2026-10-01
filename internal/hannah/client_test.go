package hannah

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	hannahproto "github.com/NurPech/hannah-proto-go/v5"
	v1pb "github.com/NurPech/hannah-proto-go/v5/hannahv1"
	pb "github.com/NurPech/hannah-proto-go/v5/hannahv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gitlab.com/gessinger/hannah-grpc-lib/go/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// recorder collects what a fake Core received, whichever API generation it serves.
type recorder struct {
	mu            sync.Mutex
	registrations []*pb.LogCollectorRegister
	versions      []string
	services      []string
	dropAfterAck  bool
}

func (r *recorder) record(ctx context.Context, service string, reg *pb.LogCollectorRegister) bool {
	md, _ := metadata.FromIncomingContext(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registrations = append(r.registrations, reg)
	r.versions = append(r.versions, md.Get(client.ProtoVersionMetadataKey)...)
	r.services = append(r.services, service)
	return r.dropAfterAck
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.registrations)
}

// fakeHannah is a Core that serves hannah.v2.
type fakeHannah struct {
	pb.UnimplementedHannahServiceServer
	rec *recorder
}

func (f *fakeHannah) GetSatellites(context.Context, *pb.Empty) (*pb.GetSatellitesResponse, error) {
	return &pb.GetSatellitesResponse{}, nil
}

func (f *fakeHannah) LogCollectorConnect(stream grpc.BidiStreamingServer[pb.LogCollectorMessage, pb.LogCollectorCommand]) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	drop := f.rec.record(stream.Context(), client.CurrentService, msg.GetRegister())
	if err := stream.Send(&pb.LogCollectorCommand{
		Command: &pb.LogCollectorCommand_Registered{Registered: &pb.LogCollectorRegistered{}},
	}); err != nil {
		return err
	}
	if drop {
		return nil // simulate Hannah restarting
	}
	<-stream.Context().Done()
	return nil
}

// v1Hannah is a Core too old for hannah.v2: it only serves hannah.v1.
type v1Hannah struct {
	v1pb.UnimplementedHannahServiceServer
	rec *recorder
}

func (f *v1Hannah) LogCollectorConnect(stream grpc.BidiStreamingServer[v1pb.LogCollectorMessage, v1pb.LogCollectorCommand]) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	r := msg.GetRegister()
	f.rec.record(stream.Context(), client.HannahService.Previous, &pb.LogCollectorRegister{
		Instance: r.GetInstance(), Host: r.GetHost(), Port: r.GetPort(), Version: r.GetVersion(),
	})
	if err := stream.Send(&v1pb.LogCollectorCommand{
		Command: &v1pb.LogCollectorCommand_Registered{Registered: &v1pb.LogCollectorRegistered{}},
	}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return nil
}

func startFake(t *testing.T, register func(*grpc.Server)) grpc.DialOption {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	register(srv)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) })
}

func TestRegistersWithHannah(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(*grpc.Server, *recorder)
		service  string
	}{
		{"hannah.v2", func(s *grpc.Server, r *recorder) { pb.RegisterHannahServiceServer(s, &fakeHannah{rec: r}) }, client.CurrentService},
		{"Core without hannah.v2", func(s *grpc.Server, r *recorder) { v1pb.RegisterHannahServiceServer(s, &v1Hannah{rec: r}) }, client.HannahService.Previous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			dialer := startFake(t, func(s *grpc.Server) { tc.register(s, rec) })

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := New("passthrough:///bufnet", Registration{Instance: "main", Host: "10.0.0.5", Port: 50060, Version: "0.1.0"}, dialer)
			go c.Run(ctx)

			require.Eventually(t, func() bool { return rec.count() == 1 }, 2*time.Second, 10*time.Millisecond)
			rec.mu.Lock()
			defer rec.mu.Unlock()
			reg := rec.registrations[0]
			assert.Equal(t, "main", reg.GetInstance())
			assert.Equal(t, "10.0.0.5", reg.GetHost())
			assert.Equal(t, int32(50060), reg.GetPort())
			assert.Equal(t, []string{strconv.Itoa(hannahproto.ProtoVersion)}, rec.versions)
			assert.Equal(t, []string{tc.service}, rec.services)
		})
	}
}

func TestReregistersAfterStreamDrops(t *testing.T) {
	rec := &recorder{dropAfterAck: true}
	dialer := startFake(t, func(s *grpc.Server) { pb.RegisterHannahServiceServer(s, &fakeHannah{rec: rec}) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New("passthrough:///bufnet", Registration{Instance: "main"}, dialer)
	go c.Run(ctx)

	// First reconnect happens after the 1 s backoff.
	require.Eventually(t, func() bool { return rec.count() >= 2 }, 3*time.Second, 20*time.Millisecond)
}
