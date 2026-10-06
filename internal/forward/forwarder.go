// Package forward passes every entry the collector accepts on to a syslog receiver (in practice
// Alloy's loki.source.syslog), as RFC 5424 over TCP or UDP. It is a pure egress: the collector
// stays a flight recorder, the receiver is where logs can be searched.
//
// The target must never slow the collector down, so entries wait in a bounded queue that drops
// its oldest entry when full, and the sender reconnects on its own.
package forward

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
)

const (
	// DefaultQueueSize is how many entries wait for the target before the oldest is dropped.
	DefaultQueueSize = 10000

	maxBatch      = 200
	dialTimeout   = 5 * time.Second
	writeTimeout  = 10 * time.Second
	flushTimeout  = 2 * time.Second
	minBackoff    = time.Second
	maxBackoff    = 30 * time.Second
	dropLogPeriod = time.Minute
)

// Config says where the entries go.
type Config struct {
	Network   string // "tcp" or "udp"
	Address   string // host:port
	QueueSize int    // 0 = DefaultQueueSize
}

// Sources resolves the source of an entry (the store).
type Sources interface {
	AllSources(ctx context.Context) (map[int64]store.Source, error)
}

// Forwarder queues entries (Offer) and sends them (Run).
type Forwarder struct {
	cfg     Config
	sources Sources

	mu       sync.Mutex
	ring     []store.Entry
	head, n  int
	dropped  uint64 // since the last report
	notify   chan struct{}
	lastDrop time.Time // of the last report

	known map[int64]store.Source // only touched by Run
}

// New creates a forwarder; Run starts it.
func New(cfg Config, sources Sources) *Forwarder {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultQueueSize
	}
	return &Forwarder{
		cfg:     cfg,
		sources: sources,
		ring:    make([]store.Entry, cfg.QueueSize),
		notify:  make(chan struct{}, 1),
		known:   map[int64]store.Source{},
	}
}

// Offer queues an entry. It never blocks: if the queue is full the oldest entry is dropped.
// Safe to call from several goroutines.
func (f *Forwarder) Offer(e store.Entry) {
	f.mu.Lock()
	if f.n == len(f.ring) {
		f.head = (f.head + 1) % len(f.ring)
		f.n--
		f.dropped++
	}
	f.ring[(f.head+f.n)%len(f.ring)] = e
	f.n++
	f.mu.Unlock()

	select {
	case f.notify <- struct{}{}:
	default:
	}
}

// take removes up to limit entries, oldest first.
func (f *Forwarder) take(limit int) []store.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := min(limit, f.n)
	out := make([]store.Entry, count)
	for i := range out {
		out[i] = f.ring[f.head]
		f.head = (f.head + 1) % len(f.ring)
	}
	f.n -= count
	return out
}

// reportDrops says now and then how many entries the full queue dropped. Only a summary every
// dropLogPeriod: the line itself is an entry for the queue, a message per drop would feed on itself.
func (f *Forwarder) reportDrops() {
	f.mu.Lock()
	dropped := f.dropped
	if dropped == 0 || time.Since(f.lastDrop) < dropLogPeriod {
		f.mu.Unlock()
		return
	}
	f.dropped, f.lastDrop = 0, time.Now()
	f.mu.Unlock()
	slog.Warn("forwarding queue full, dropped the oldest entries", "dropped", dropped, "target", f.cfg.Address)
}

// Run sends until ctx is cancelled, then tries once to flush what is left. Blocks.
func (f *Forwarder) Run(ctx context.Context) {
	var conn net.Conn
	defer func() {
		if conn != nil {
			f.flush(conn)
			conn.Close()
		}
	}()

	up := true // the first failure is worth a line, a retry every few seconds is not
	for {
		batch := f.take(maxBatch)
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-f.notify:
				continue
			}
		}
		payload := f.encode(ctx, batch)

		backoff := minBackoff
		for len(payload.lines) > 0 {
			if conn == nil {
				c, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, f.cfg.Network, f.cfg.Address)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					if up {
						slog.Warn("forwarding target unreachable, entries wait in the queue", "target", f.cfg.Address, "err", err)
						up = false
					}
					f.reportDrops()
					if !sleep(ctx, backoff) {
						return
					}
					backoff = min(backoff*2, maxBackoff)
					continue
				}
				conn = c
				if !up {
					slog.Info("forwarding target reachable again", "target", f.cfg.Address)
					up = true
				}
			}
			if err := f.send(conn, payload); err != nil {
				conn.Close()
				conn = nil
				if ctx.Err() != nil {
					return
				}
				if up {
					slog.Warn("forwarding to the target failed, reconnecting", "target", f.cfg.Address, "err", err)
					up = false
				}
				continue
			}
			break
		}
		f.reportDrops()
	}
}

// flush sends what is still queued over conn, a last effort on shutdown.
func (f *Forwarder) flush(conn net.Conn) {
	_ = conn.SetWriteDeadline(time.Now().Add(flushTimeout))
	for {
		batch := f.take(maxBatch)
		if len(batch) == 0 {
			return
		}
		if err := f.send(conn, f.encode(context.Background(), batch)); err != nil {
			return
		}
	}
}

// encoded is a batch ready to go out: one entry per line, already framed for TCP.
type encoded struct {
	lines [][]byte
}

// encode renders the batch. An entry without a message is skipped, a receiver such as Alloy
// refuses an empty one by default; an entry whose source can't be resolved is skipped too.
func (f *Forwarder) encode(ctx context.Context, batch []store.Entry) encoded {
	var out encoded
	for _, e := range batch {
		if text(e.Message) == "" {
			continue
		}
		src, ok := f.source(ctx, e.SourceID)
		if !ok {
			continue
		}
		line := Format(e, src)
		if f.cfg.Network == "tcp" {
			line = Frame(line)
		}
		out.lines = append(out.lines, line)
	}
	return out
}

func (f *Forwarder) source(ctx context.Context, id int64) (store.Source, bool) {
	if src, ok := f.known[id]; ok {
		return src, true
	}
	all, err := f.sources.AllSources(ctx)
	if err != nil {
		return store.Source{}, false
	}
	f.known = all
	src, ok := f.known[id]
	return src, ok
}

// send writes the batch: a TCP stream in one write, UDP as one datagram per message.
func (f *Forwarder) send(conn net.Conn, payload encoded) error {
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if f.cfg.Network == "tcp" {
		_, err := conn.Write(bytes.Join(payload.lines, nil))
		return err
	}
	for _, line := range payload.lines {
		if _, err := conn.Write(line); err != nil {
			return err
		}
	}
	return nil
}

// sleep waits for d, false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
