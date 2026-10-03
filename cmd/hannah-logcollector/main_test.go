package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	hannahlog "gitlab.com/gessinger/hannah-grpc-lib/go/logging"
	"google.golang.org/grpc"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/server"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
)

func TestSelfAddress(t *testing.T) {
	cases := map[string]struct {
		addr net.Addr
		want string
	}{
		"unspecified IPv6 (listen :50060)": {&net.TCPAddr{IP: net.IPv6unspecified, Port: 50060}, "127.0.0.1:50060"},
		"unspecified IPv4":                 {&net.TCPAddr{IP: net.IPv4zero, Port: 50060}, "127.0.0.1:50060"},
		"no IP at all":                     {&net.TCPAddr{Port: 50060}, "127.0.0.1:50060"},
		"specific IPv4":                    {&net.TCPAddr{IP: net.ParseIP("192.168.8.69"), Port: 50060}, "192.168.8.69:50060"},
		"specific IPv6":                    {&net.TCPAddr{IP: net.ParseIP("::1"), Port: 50060}, "[::1]:50060"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, selfAddress(c.addr))
		})
	}
}

// The collector's own records go through the library into its own LogService and end up in
// its own store, without Hannah Core (the static address, #11).
func TestOwnLogsEndUpInTheOwnStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "logs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	writer := store.NewWriter(st, 100, 20*time.Millisecond)
	writerDone := make(chan struct{})
	go func() { writer.Run(ctx); close(writerDone) }()
	t.Cleanup(func() { cancel(); <-writerDone })

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	server.Register(srv, server.New(st, writer, dir))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	shipping, err := hannahlog.New(slog.NewTextHandler(io.Discard, nil), hannahlog.Options{
		Component: "logcollector", Version: "9.9.9", Instance: "test-host",
	})
	require.NoError(t, err)
	log := slog.New(shipping.Handler())
	log.Info("logged before connect")

	shipping.Connect("", selfAddress(lis.Addr()))
	log.Warn("logged after connect")

	// Wait until the lines arrived before closing: a Close right after Connect can end the
	// shipper before it opened its stream.
	stored := func() []string {
		require.NoError(t, writer.Sync(ctx))
		var messages []string
		require.NoError(t, st.EachEntry(ctx, store.Filter{Components: []string{"logcollector"}}, func(e store.Entry) error {
			messages = append(messages, e.Message)
			return nil
		}))
		return messages
	}
	require.Eventually(t, func() bool { return len(stored()) == 2 }, 10*time.Second, 20*time.Millisecond)
	shipping.Close(5 * time.Second)

	assert.Equal(t, []string{"logged before connect", "logged after connect"}, stored())

	sources, err := st.Sources(ctx)
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "logcollector", sources[0].Component)
	assert.Equal(t, "test-host", sources[0].Instance)
	assert.Equal(t, "9.9.9", sources[0].Version)
}
