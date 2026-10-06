package syslog

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	pb "github.com/NurPech/hannah-proto-go/v5/hannahv2"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
)

// maxDatagram is the largest UDP payload there is.
const maxDatagram = 65535

// maxSources bounds the source cache; a sender that keeps inventing hostnames can't grow it forever.
const maxSources = 1024

// fallbackComponent names the source of a message without an app name.
const fallbackComponent = "syslog"

// Sources registers the senders in the store.
type Sources interface {
	UpsertSource(ctx context.Context, component, instance, version string) (int64, error)
}

// Sink takes the entries, in practice the store's batching writer, the same one the Ship streams use.
type Sink interface {
	Add(ctx context.Context, e store.Entry) error
}

type sourceKey struct{ component, instance string }

// Receiver reads syslog datagrams and hands them to the sink. The component of a source is the
// app name of its messages (the satellites send "hannah-esp"), its instance the hostname
// (their device ID), and its version stays empty. Run is the only goroutine that touches the
// receiver after Listen.
type Receiver struct {
	conn    net.PacketConn
	sources Sources
	sink    Sink
	now     func() time.Time

	ids       map[sourceKey]int64
	malformed uint64
}

// Listen opens the UDP socket on addr (":5514").
func Listen(addr string, sources Sources, sink Sink) (*Receiver, error) {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}
	return &Receiver{conn: conn, sources: sources, sink: sink, now: time.Now, ids: map[sourceKey]int64{}}, nil
}

// Addr is the address the receiver listens on.
func (r *Receiver) Addr() net.Addr { return r.conn.LocalAddr() }

// Run reads datagrams until ctx is cancelled, then closes the socket. A datagram that can't be
// parsed is dropped and counted, it never stops the receiver. Blocks.
func (r *Receiver) Run(ctx context.Context) {
	go func() {
		<-ctx.Done()
		r.conn.Close()
	}()

	buf := make([]byte, maxDatagram)
	for {
		n, from, err := r.conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Warn("reading syslog datagram", "err", err)
			continue
		}
		r.handle(ctx, buf[:n], from)
	}
}

func (r *Receiver) handle(ctx context.Context, packet []byte, from net.Addr) {
	msg, err := Parse(packet)
	if err != nil {
		r.malformed++
		// The first one and then every thousandth: a misconfigured sender must not flood the log.
		if r.malformed == 1 || r.malformed%1000 == 0 {
			slog.Warn("dropping malformed syslog datagram", "from", from.String(), "bytes", len(packet), "dropped", r.malformed)
		}
		return
	}

	component, instance := msg.AppName, msg.Hostname
	if component == "" {
		component = fallbackComponent
	}
	if instance == "" {
		instance = hostOf(from)
	}
	id, err := r.sourceID(ctx, component, instance)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("registering syslog source", "component", component, "instance", instance, "err", err)
		}
		return
	}

	ts := msg.Time
	if ts.IsZero() {
		ts = r.now()
	}
	// Add only fails when ctx is cancelled, on shutdown.
	_ = r.sink.Add(ctx, store.Entry{
		SourceID:    id,
		TimestampMs: ts.UnixMilli(),
		Level:       int32(levelFor(msg.Severity)),
		Message:     msg.Text,
		Category:    int32(pb.LogCategory_LOG_CATEGORY_GENERAL),
	})
}

func (r *Receiver) sourceID(ctx context.Context, component, instance string) (int64, error) {
	key := sourceKey{component, instance}
	if id, ok := r.ids[key]; ok {
		return id, nil
	}
	id, err := r.sources.UpsertSource(ctx, component, instance, "")
	if err != nil {
		return 0, err
	}
	if len(r.ids) >= maxSources {
		clear(r.ids)
	}
	r.ids[key] = id
	return id, nil
}

// levelFor maps a syslog severity to the log level of the shipping API.
func levelFor(severity int) pb.LogLevel {
	switch {
	case severity <= 2: // emergency, alert, critical
		return pb.LogLevel_LOG_LEVEL_CRITICAL
	case severity == 3:
		return pb.LogLevel_LOG_LEVEL_ERROR
	case severity == 4:
		return pb.LogLevel_LOG_LEVEL_WARNING
	case severity <= 6: // notice, informational
		return pb.LogLevel_LOG_LEVEL_INFO
	default:
		return pb.LogLevel_LOG_LEVEL_DEBUG
	}
}

// hostOf is the sender's IP, the instance name of a message that came without a hostname.
func hostOf(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
