package syslog

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/NurPech/hannah-proto-go/v5/hannahv2"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
)

type fakeSources struct {
	mu    sync.Mutex
	ids   map[sourceKey]int64
	calls int
}

func (f *fakeSources) UpsertSource(_ context.Context, component, instance, version string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if version != "" {
		panic("a syslog source has no version")
	}
	key := sourceKey{component, instance}
	if _, ok := f.ids[key]; !ok {
		f.ids[key] = int64(len(f.ids) + 1)
	}
	return f.ids[key], nil
}

type chanSink chan store.Entry

func (c chanSink) Add(_ context.Context, e store.Entry) error { c <- e; return nil }

// start runs a receiver on a free local port; the returned function sends one datagram to it.
func start(t *testing.T, now func() time.Time) (*fakeSources, chanSink, func(string)) {
	t.Helper()
	sources := &fakeSources{ids: map[sourceKey]int64{}}
	sink := make(chanSink, 16)
	r, err := Listen("127.0.0.1:0", sources, sink)
	if err != nil {
		t.Fatal(err)
	}
	if now != nil {
		r.now = now
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("the receiver did not stop on cancel")
		}
	})

	conn, err := net.Dial("udp", r.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return sources, sink, func(packet string) {
		if _, err := conn.Write([]byte(packet)); err != nil {
			t.Fatal(err)
		}
	}
}

func next(t *testing.T, sink chanSink) store.Entry {
	t.Helper()
	select {
	case e := <-sink:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no entry arrived")
		return store.Entry{}
	}
}

func TestReceiverStoresASatelliteLine(t *testing.T) {
	fixed := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	_, sink, send := start(t, func() time.Time { return fixed })

	send("<131>1 - e072a1d01adc hannah-esp - - - E (99) mic: i2s failed")
	e := next(t, sink)

	if e.SourceID != 1 || e.TimestampMs != fixed.UnixMilli() || e.Message != "E (99) mic: i2s failed" {
		t.Fatalf("got %+v", e)
	}
	if e.Level != int32(pb.LogLevel_LOG_LEVEL_ERROR) || e.Category != int32(pb.LogCategory_LOG_CATEGORY_GENERAL) {
		t.Fatalf("level %d, category %d", e.Level, e.Category)
	}
}

func TestReceiverUsesTheSenderTimestampWhenThereIsOne(t *testing.T) {
	_, sink, send := start(t, nil)
	send("<14>1 2026-10-06T12:30:05Z host app - - - x")
	if e := next(t, sink); e.TimestampMs != time.Date(2026, 10, 6, 12, 30, 5, 0, time.UTC).UnixMilli() {
		t.Fatalf("timestamp %d", e.TimestampMs)
	}
}

func TestReceiverRegistersEachSenderOnce(t *testing.T) {
	sources, sink, send := start(t, nil)
	send("<14>1 - dev-a hannah-esp - - - one")
	send("<14>1 - dev-a hannah-esp - - - two")
	send("<14>1 - dev-b hannah-esp - - - three")
	a1, a2, b := next(t, sink), next(t, sink), next(t, sink)

	if a1.SourceID != a2.SourceID || a1.SourceID == b.SourceID {
		t.Fatalf("source IDs %d %d %d", a1.SourceID, a2.SourceID, b.SourceID)
	}
	sources.mu.Lock()
	defer sources.mu.Unlock()
	if sources.calls != 2 {
		t.Fatalf("UpsertSource called %d times, want 2", sources.calls)
	}
}

func TestReceiverNamesASenderWithoutHostnameAndAppName(t *testing.T) {
	sources, sink, send := start(t, nil)
	send("<14>1 - - - - - - anonymous")
	next(t, sink)

	sources.mu.Lock()
	defer sources.mu.Unlock()
	if _, ok := sources.ids[sourceKey{fallbackComponent, "127.0.0.1"}]; !ok {
		t.Fatalf("sources %v", sources.ids)
	}
}

func TestReceiverSurvivesMalformedDatagrams(t *testing.T) {
	_, sink, send := start(t, nil)
	send("not syslog at all")
	send("<34>Oct 11 22:14:15 mymachine su: old format")
	send("")
	send("<14>1 - dev hannah-esp - - - still alive")
	if e := next(t, sink); e.Message != "still alive" {
		t.Fatalf("got %+v", e)
	}
}

func TestLevelFor(t *testing.T) {
	for severity, want := range map[int]pb.LogLevel{
		0: pb.LogLevel_LOG_LEVEL_CRITICAL,
		1: pb.LogLevel_LOG_LEVEL_CRITICAL,
		2: pb.LogLevel_LOG_LEVEL_CRITICAL,
		3: pb.LogLevel_LOG_LEVEL_ERROR,
		4: pb.LogLevel_LOG_LEVEL_WARNING,
		5: pb.LogLevel_LOG_LEVEL_INFO,
		6: pb.LogLevel_LOG_LEVEL_INFO,
		7: pb.LogLevel_LOG_LEVEL_DEBUG,
	} {
		if got := levelFor(severity); got != want {
			t.Errorf("severity %d: %v, want %v", severity, got, want)
		}
	}
}
