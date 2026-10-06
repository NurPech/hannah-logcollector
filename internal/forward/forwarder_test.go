package forward

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	pb "github.com/NurPech/hannah-proto-go/v5/hannahv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/syslog"
)

type fakeSources map[int64]store.Source

func (f fakeSources) AllSources(context.Context) (map[int64]store.Source, error) { return f, nil }

var sources = fakeSources{1: satellite}

func line(i int) store.Entry {
	e := entry(pb.LogLevel_LOG_LEVEL_INFO, "line "+strconv.Itoa(i))
	e.TimestampMs += int64(i)
	return e
}

// tcpTarget is a syslog receiver over TCP that reads octet-counted messages.
type tcpTarget struct {
	lis  net.Listener
	mu   sync.Mutex
	got  []string
	conn []net.Conn
}

func listenTCP(t *testing.T, addr string) *tcpTarget {
	t.Helper()
	lis, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	target := &tcpTarget{lis: lis}
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			target.mu.Lock()
			target.conn = append(target.conn, conn)
			target.mu.Unlock()
			go target.read(conn)
		}
	}()
	t.Cleanup(target.close)
	return target
}

func (s *tcpTarget) read(conn net.Conn) {
	r := bufio.NewReader(conn)
	for {
		count, err := r.ReadString(' ')
		if err != nil {
			return
		}
		n, err := strconv.Atoi(count[:len(count)-1])
		if err != nil {
			return
		}
		msg := make([]byte, n)
		if _, err := io.ReadFull(r, msg); err != nil {
			return
		}
		s.mu.Lock()
		s.got = append(s.got, string(msg))
		s.mu.Unlock()
	}
}

func (s *tcpTarget) close() {
	s.lis.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conn {
		c.Close()
	}
}

func (s *tcpTarget) addr() string { return s.lis.Addr().String() }

func (s *tcpTarget) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

func (s *tcpTarget) waitFor(t *testing.T, count int) []string {
	t.Helper()
	require.Eventually(t, func() bool { return len(s.received()) >= count }, 5*time.Second, 10*time.Millisecond)
	return s.received()
}

func run(t *testing.T, cfg Config) (*Forwarder, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	f := New(cfg, sources)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the forwarder did not stop")
		}
	})
	return f, cancel, done
}

func TestForwardsOverTCPInOrder(t *testing.T) {
	target := listenTCP(t, "127.0.0.1:0")
	f, _, _ := run(t, Config{Network: "tcp", Address: target.addr()})

	f.Offer(line(1))
	f.Offer(entry(pb.LogLevel_LOG_LEVEL_INFO, ""))                                 // empty message: skipped
	f.Offer(store.Entry{SourceID: 99, TimestampMs: 1, Message: "from a stranger"}) // unknown source: skipped
	f.Offer(line(2))

	got := target.waitFor(t, 2)
	require.Len(t, got, 2)
	for i, want := range []string{"line 1", "line 2"} {
		msg, err := syslog.Parse([]byte(got[i]))
		require.NoError(t, err)
		assert.Equal(t, want, msg.Text)
		assert.Equal(t, "e072a1d01adc", msg.Hostname)
		assert.Equal(t, "hannah-esp", msg.AppName)
	}
}

func TestReconnectsAfterTheTargetWasAway(t *testing.T) {
	first := listenTCP(t, "127.0.0.1:0")
	addr := first.addr()
	f, _, _ := run(t, Config{Network: "tcp", Address: addr})

	f.Offer(line(1))
	first.waitFor(t, 1)
	first.close() // the target goes away, and comes back on the same address
	second := listenTCP(t, addr)

	// The first write into the dead connection may still be accepted by the system and get lost,
	// so keep offering until one arrives at the new target.
	require.Eventually(t, func() bool {
		f.Offer(line(2))
		return len(second.received()) > 0
	}, 10*time.Second, 50*time.Millisecond)
}

func TestWhileTheTargetIsDownOfferNeverBlocksAndTheQueueStaysBounded(t *testing.T) {
	down, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := down.Addr().String()
	down.Close() // nobody listens there any more

	f, _, _ := run(t, Config{Network: "tcp", Address: addr, QueueSize: 10})

	finished := make(chan struct{})
	go func() {
		for i := 0; i < 100000; i++ {
			f.Offer(line(i))
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Offer blocked while the target is down")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	assert.LessOrEqual(t, f.n, 10)
	assert.Greater(t, f.dropped, uint64(0))
}

func TestWhenFullTheOldestEntryIsDropped(t *testing.T) {
	f := New(Config{Network: "tcp", Address: "127.0.0.1:1", QueueSize: 3}, sources)
	for i := 0; i < 5; i++ {
		f.Offer(line(i))
	}

	got := f.take(10)
	require.Len(t, got, 3)
	assert.Equal(t, []int64{line(2).TimestampMs, line(3).TimestampMs, line(4).TimestampMs},
		[]int64{got[0].TimestampMs, got[1].TimestampMs, got[2].TimestampMs})
	assert.Equal(t, uint64(2), f.dropped)
}

func TestForwardsOverUDPAsOneDatagramPerMessage(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { pc.Close() })

	f, _, _ := run(t, Config{Network: "udp", Address: pc.LocalAddr().String()})
	f.Offer(line(1))
	f.Offer(line(2))

	for _, want := range []string{"line 1", "line 2"} {
		buf := make([]byte, 65535)
		require.NoError(t, pc.SetReadDeadline(time.Now().Add(5*time.Second)))
		n, _, err := pc.ReadFrom(buf)
		require.NoError(t, err)
		msg, err := syslog.Parse(buf[:n]) // no octet count in front: a datagram is its own frame
		require.NoError(t, err)
		assert.Equal(t, want, msg.Text)
	}
}

func TestWhatIsLeftIsSentWhenTheForwarderStops(t *testing.T) {
	target := listenTCP(t, "127.0.0.1:0")
	f, cancel, done := run(t, Config{Network: "tcp", Address: target.addr()})

	f.Offer(line(0))
	target.waitFor(t, 1) // connected
	for i := 1; i <= 50; i++ {
		f.Offer(line(i))
	}
	cancel()
	<-done

	assert.Len(t, target.waitFor(t, 51), 51)
}
