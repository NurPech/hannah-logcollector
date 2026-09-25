package hannah

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/NurPech/hannah-proto-go/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// fakeHannah records LogCollectorConnect registrations.
type fakeHannah struct {
	pb.UnimplementedHannahServiceServer

	mu            sync.Mutex
	registrations []*pb.LogCollectorRegister
	versions      []string
	dropAfterAck  bool
}

func (f *fakeHannah) LogCollectorConnect(stream grpc.BidiStreamingServer[pb.LogCollectorMessage, pb.LogCollectorCommand]) error {
	md, _ := metadata.FromIncomingContext(stream.Context())

	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.registrations = append(f.registrations, msg.GetRegister())
	f.versions = append(f.versions, md.Get(ProtoVersionMetadataKey)...)
	drop := f.dropAfterAck
	f.mu.Unlock()

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

func (f *fakeHannah) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registrations)
}

func startFake(t *testing.T, fake *fakeHannah) grpc.DialOption {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterHannahServiceServer(srv, fake)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) })
}

func TestRegistersWithHannah(t *testing.T) {
	fake := &fakeHannah{}
	dialer := startFake(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := New("passthrough:///bufnet", Registration{Instance: "main", Host: "10.0.0.5", Port: 50060, Version: "0.1.0"}, dialer)
	go client.Run(ctx)

	require.Eventually(t, func() bool { return fake.count() == 1 }, 2*time.Second, 10*time.Millisecond)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	reg := fake.registrations[0]
	assert.Equal(t, "main", reg.GetInstance())
	assert.Equal(t, "10.0.0.5", reg.GetHost())
	assert.Equal(t, int32(50060), reg.GetPort())
	assert.Equal(t, []string{protoVersion}, fake.versions)
}

func TestReregistersAfterStreamDrops(t *testing.T) {
	fake := &fakeHannah{dropAfterAck: true}
	dialer := startFake(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := New("passthrough:///bufnet", Registration{Instance: "main"}, dialer)
	go client.Run(ctx)

	// First reconnect happens after the 1 s backoff.
	require.Eventually(t, func() bool { return fake.count() >= 2 }, 3*time.Second, 20*time.Millisecond)
}
