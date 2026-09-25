package store

import (
	"context"
	"log/slog"
	"time"
)

// Writer batches incoming entries so they're written in one transaction per batch
// instead of one per log line. Entries from all Ship streams go through it.
type Writer struct {
	store      *Store
	batchSize  int
	flushEvery time.Duration
	requests   chan writeRequest
}

type writeRequest struct {
	entry *Entry
	sync  chan error // set for Sync: answered once everything queued before it is written
}

func NewWriter(store *Store, batchSize int, flushEvery time.Duration) *Writer {
	return &Writer{
		store:      store,
		batchSize:  batchSize,
		flushEvery: flushEvery,
		requests:   make(chan writeRequest, batchSize),
	}
}

// Run writes batches until ctx is cancelled, then flushes what's left. Blocks.
func (w *Writer) Run(ctx context.Context) {
	ticker := time.NewTicker(w.flushEvery)
	defer ticker.Stop()

	batch := make([]Entry, 0, w.batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		// Background context: a shutdown must not abort the final flush.
		err := w.store.InsertEntries(context.Background(), batch)
		if err != nil {
			slog.Error("writing log entries", "count", len(batch), "err", err)
		}
		batch = batch[:0]
		return err
	}

	for {
		select {
		case <-ctx.Done():
			w.drain(&batch)
			_ = flush()
			return
		case req := <-w.requests:
			if req.entry != nil {
				batch = append(batch, *req.entry)
				if len(batch) >= w.batchSize {
					_ = flush()
				}
			}
			if req.sync != nil {
				req.sync <- flush()
			}
		case <-ticker.C:
			_ = flush()
		}
	}
}

// drain picks up entries still queued at shutdown.
func (w *Writer) drain(batch *[]Entry) {
	for {
		select {
		case req := <-w.requests:
			if req.entry != nil {
				*batch = append(*batch, *req.entry)
			}
			if req.sync != nil {
				req.sync <- nil
			}
		default:
			return
		}
	}
}

// Add queues an entry. Blocks while the queue is full (backpressure onto the stream).
func (w *Writer) Add(ctx context.Context, e Entry) error {
	select {
	case w.requests <- writeRequest{entry: &e}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Sync returns once every entry queued before the call has been written.
func (w *Writer) Sync(ctx context.Context) error {
	done := make(chan error, 1)
	select {
	case w.requests <- writeRequest{sync: done}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
