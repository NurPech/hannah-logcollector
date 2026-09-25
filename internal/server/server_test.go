package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/NurPech/hannah-proto-go/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
)

// startServer runs a LogService on an in-memory connection and returns a client for it.
func startServer(t *testing.T) pb.LogServiceClient {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "logs.db"))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	w := store.NewWriter(st, 100, 50*time.Millisecond)
	writerDone := make(chan struct{})
	go func() { w.Run(ctx); close(writerDone) }()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterLogServiceServer(srv, New(st, w, dir))
	go srv.Serve(lis)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		conn.Close()
		srv.Stop()
		cancel()
		<-writerDone
		st.Close()
	})
	return pb.NewLogServiceClient(conn)
}

func ship(t *testing.T, client pb.LogServiceClient, msgs ...*pb.ShipMessage) (*pb.ShipAck, error) {
	t.Helper()
	stream, err := client.Ship(context.Background())
	require.NoError(t, err)
	for _, m := range msgs {
		require.NoError(t, stream.Send(m))
	}
	return stream.CloseAndRecv()
}

func hello(component, instance string) *pb.ShipMessage {
	return &pb.ShipMessage{Payload: &pb.ShipMessage_Hello{Hello: &pb.ShipHello{
		Component: component, Instance: instance, Version: "1.0.0",
	}}}
}

func entry(ts int64, msg string, category pb.LogCategory) *pb.ShipMessage {
	return &pb.ShipMessage{Payload: &pb.ShipMessage_Entry{Entry: &pb.LogEntry{
		TimestampMs: ts, Level: pb.LogLevel_LOG_LEVEL_INFO, Logger: "test", Message: msg, Category: category,
	}}}
}

func exportFiles(t *testing.T, client pb.LogServiceClient, req *pb.ExportRequest) map[string]string {
	t.Helper()
	stream, err := client.Export(context.Background(), req)
	require.NoError(t, err)

	var data bytes.Buffer
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		data.Write(chunk.GetData())
	}

	gz, err := gzip.NewReader(&data)
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		require.NoError(t, err)
		content, _ := io.ReadAll(tr)
		files[hdr.Name] = string(content)
	}
}

func TestShipStoresEntriesAndReportsSource(t *testing.T) {
	client := startServer(t)

	ack, err := ship(t, client,
		hello("core", "pi"),
		entry(1000, "one", pb.LogCategory_LOG_CATEGORY_GENERAL),
		entry(2000, "two", pb.LogCategory_LOG_CATEGORY_GENERAL),
		&pb.ShipMessage{Payload: &pb.ShipMessage_Gap{Gap: &pb.LogGap{Dropped: 5, FromMs: 100, ToMs: 200}}},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), ack.GetAccepted())

	resp, err := client.GetSources(context.Background(), &pb.Empty{})
	require.NoError(t, err)
	require.Len(t, resp.GetSources(), 1)
	src := resp.GetSources()[0]
	assert.Equal(t, "core", src.GetComponent())
	assert.Equal(t, "1.0.0", src.GetVersion())
	assert.Equal(t, int64(1000), src.GetOldestMs())
	assert.Equal(t, int64(2000), src.GetNewestMs())
	assert.Equal(t, int64(2), src.GetEntries())
}

func TestShipWithoutHelloIsRejected(t *testing.T) {
	client := startServer(t)

	_, err := ship(t, client, entry(1000, "one", pb.LogCategory_LOG_CATEGORY_GENERAL))
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestExportExcludesTranscripts(t *testing.T) {
	client := startServer(t)
	_, err := ship(t, client,
		hello("core", "pi"),
		entry(1000, "intent TurnOn", pb.LogCategory_LOG_CATEGORY_GENERAL),
		entry(2000, "mach das Licht an", pb.LogCategory_LOG_CATEGORY_TRANSCRIPT),
	)
	require.NoError(t, err)

	files := exportFiles(t, client, &pb.ExportRequest{
		ExcludeCategories: []pb.LogCategory{pb.LogCategory_LOG_CATEGORY_TRANSCRIPT},
	})
	assert.Contains(t, files["core-pi.log"], "intent TurnOn")
	assert.NotContains(t, files["core-pi.log"], "mach das Licht an")
	assert.Contains(t, files, "manifest.json")
}

func TestExportLargerThanOneChunk(t *testing.T) {
	client := startServer(t)

	// ~600 KB of random letters: even gzipped well above one 256 KB chunk.
	rng := rand.New(rand.NewPCG(1, 2))
	msgs := []*pb.ShipMessage{hello("core", "pi")}
	for i := 0; i < 3000; i++ {
		line := make([]byte, 200)
		for j := range line {
			line[j] = byte('a' + rng.IntN(26))
		}
		msgs = append(msgs, entry(int64(i), string(line), pb.LogCategory_LOG_CATEGORY_GENERAL))
	}
	_, err := ship(t, client, msgs...)
	require.NoError(t, err)

	stream, err := client.Export(context.Background(), &pb.ExportRequest{})
	require.NoError(t, err)
	chunks := 0
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		chunks++
	}
	assert.Greater(t, chunks, 1)

	files := exportFiles(t, client, &pb.ExportRequest{})
	assert.Equal(t, 3000, bytes.Count([]byte(files["core-pi.log"]), []byte("\n")))
}
