package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "logs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertSourceKeepsIDAndUpdatesVersion(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	id1, err := s.UpsertSource(ctx, "core", "pi", "0.85.0")
	require.NoError(t, err)
	id2, err := s.UpsertSource(ctx, "core", "pi", "0.86.0")
	require.NoError(t, err)
	other, err := s.UpsertSource(ctx, "telegram", "pi", "1.0.0")
	require.NoError(t, err)

	assert.Equal(t, id1, id2)
	assert.NotEqual(t, id1, other)

	all, err := s.AllSources(ctx)
	require.NoError(t, err)
	assert.Equal(t, "0.86.0", all[id1].Version)
}

func TestSourcesReportsRangeAndCount(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	core, _ := s.UpsertSource(ctx, "core", "pi", "0.85.0")
	_, _ = s.UpsertSource(ctx, "idle", "pi", "1.0.0") // no entries → not listed

	require.NoError(t, s.InsertEntries(ctx, []Entry{
		{SourceID: core, TimestampMs: 2000, Message: "b"},
		{SourceID: core, TimestampMs: 1000, Message: "a"},
	}))

	stats, err := s.Sources(ctx)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.Equal(t, "core", stats[0].Component)
	assert.Equal(t, int64(1000), stats[0].OldestMs)
	assert.Equal(t, int64(2000), stats[0].NewestMs)
	assert.Equal(t, int64(2), stats[0].Entries)
}

func TestEachEntryFilters(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	core, _ := s.UpsertSource(ctx, "core", "pi", "")
	tg, _ := s.UpsertSource(ctx, "telegram", "pi", "")

	require.NoError(t, s.InsertEntries(ctx, []Entry{
		{SourceID: core, TimestampMs: 1000, Message: "old", Category: 1},
		{SourceID: core, TimestampMs: 2000, Message: "general", Category: 1},
		{SourceID: core, TimestampMs: 2500, Message: "transcript", Category: 2},
		{SourceID: tg, TimestampMs: 2000, Message: "telegram", Category: 1},
	}))

	var got []string
	require.NoError(t, s.EachEntry(ctx, Filter{
		SinceMs:           1500,
		Components:        []string{"core"},
		ExcludeCategories: []int32{2},
	}, func(e Entry) error {
		got = append(got, e.Message)
		return nil
	}))
	assert.Equal(t, []string{"general"}, got)
}

func TestPruneByAge(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	src, _ := s.UpsertSource(ctx, "core", "pi", "")
	now := time.UnixMilli(10 * 24 * 3600 * 1000)

	require.NoError(t, s.InsertEntries(ctx, []Entry{
		{SourceID: src, TimestampMs: now.Add(-8 * 24 * time.Hour).UnixMilli(), Message: "too old"},
		{SourceID: src, TimestampMs: now.Add(-1 * time.Hour).UnixMilli(), Message: "recent"},
	}))
	require.NoError(t, s.InsertGap(ctx, Gap{SourceID: src, Dropped: 3,
		FromMs: now.Add(-9 * 24 * time.Hour).UnixMilli(), ToMs: now.Add(-8 * 24 * time.Hour).UnixMilli()}))

	res, err := s.Prune(ctx, now, 7*24*time.Hour, 1<<30)
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.ByAge)
	assert.Equal(t, int64(0), res.BySize)

	var left []string
	require.NoError(t, s.EachEntry(ctx, Filter{}, func(e Entry) error { left = append(left, e.Message); return nil }))
	assert.Equal(t, []string{"recent"}, left)

	gaps, err := s.Gaps(ctx, Filter{})
	require.NoError(t, err)
	assert.Empty(t, gaps)
}

func TestPruneBySizeRemovesOldestFirst(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	src, _ := s.UpsertSource(ctx, "core", "pi", "")

	// ~5 MB of data, oldest first
	msg := strings.Repeat("x", 1000)
	var batch []Entry
	for i := 0; i < 5000; i++ {
		batch = append(batch, Entry{SourceID: src, TimestampMs: int64(i), Message: msg})
	}
	require.NoError(t, s.InsertEntries(ctx, batch))

	const limit = 2 << 20 // 2 MB
	res, err := s.Prune(ctx, time.UnixMilli(10_000), 24*time.Hour, limit)
	require.NoError(t, err)
	assert.Positive(t, res.BySize)

	used, err := s.UsedBytes(ctx)
	require.NoError(t, err)
	assert.LessOrEqual(t, used, int64(limit))

	stats, err := s.Sources(ctx)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.Equal(t, int64(4999), stats[0].NewestMs, "newest entries survive")
	assert.Positive(t, stats[0].OldestMs, "oldest entries were removed first")
}
