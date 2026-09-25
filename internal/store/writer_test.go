package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func countEntries(t *testing.T, s *Store) int {
	t.Helper()
	n := 0
	require.NoError(t, s.EachEntry(context.Background(), Filter{}, func(Entry) error { n++; return nil }))
	return n
}

func TestWriterSyncWritesQueuedEntries(t *testing.T) {
	s := newStore(t)
	src, _ := s.UpsertSource(context.Background(), "core", "pi", "")

	// Long flush interval + large batch: only Sync can have written the entries.
	w := NewWriter(s, 1000, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	for i := 0; i < 10; i++ {
		require.NoError(t, w.Add(ctx, Entry{SourceID: src, TimestampMs: int64(i), Message: "x"}))
	}
	require.NoError(t, w.Sync(ctx))
	assert.Equal(t, 10, countEntries(t, s))
}

func TestWriterFlushesFullBatch(t *testing.T) {
	s := newStore(t)
	src, _ := s.UpsertSource(context.Background(), "core", "pi", "")

	w := NewWriter(s, 5, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	for i := 0; i < 5; i++ {
		require.NoError(t, w.Add(ctx, Entry{SourceID: src, TimestampMs: int64(i), Message: "x"}))
	}
	assert.Eventually(t, func() bool { return countEntries(t, s) == 5 }, time.Second, 10*time.Millisecond)
}

func TestWriterFlushesOnShutdown(t *testing.T) {
	s := newStore(t)
	src, _ := s.UpsertSource(context.Background(), "core", "pi", "")

	w := NewWriter(s, 1000, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	require.NoError(t, w.Add(ctx, Entry{SourceID: src, TimestampMs: 1, Message: "x"}))
	cancel()
	<-done
	assert.Equal(t, 1, countEntries(t, s))
}
