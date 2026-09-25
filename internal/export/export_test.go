package export

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/NurPech/hannah-proto-go/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
)

var (
	general    = int32(pb.LogCategory_LOG_CATEGORY_GENERAL)
	transcript = int32(pb.LogCategory_LOG_CATEGORY_TRANSCRIPT)
	info       = int32(pb.LogLevel_LOG_LEVEL_INFO)
)

// unpack returns the archive's files by name.
func unpack(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files
		}
		require.NoError(t, err)
		content, err := io.ReadAll(tr)
		require.NoError(t, err)
		files[hdr.Name] = string(content)
	}
}

func setup(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "logs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	core, _ := st.UpsertSource(ctx, "core", "pi", "0.86.0")
	tg, _ := st.UpsertSource(ctx, "telegram", "", "1.2.0")
	require.NoError(t, st.InsertEntries(ctx, []store.Entry{
		{SourceID: core, TimestampMs: 1000, Level: info, Logger: "hannah.nlu", Message: "intent TurnOn", Category: general},
		{SourceID: core, TimestampMs: 2000, Level: info, Logger: "hannah.stt", Message: "mach das Licht an", Category: transcript},
		{SourceID: tg, TimestampMs: 1500, Level: info, Message: "bot started", Category: general},
	}))
	require.NoError(t, st.InsertGap(ctx, store.Gap{SourceID: core, Dropped: 42, FromMs: 500, ToMs: 900}))
	return st, dir
}

func TestBuildContainsOneFilePerSourceAndManifest(t *testing.T) {
	st, dir := setup(t)
	var buf bytes.Buffer
	require.NoError(t, Build(context.Background(), st, store.Filter{}, time.UnixMilli(10_000), dir, &buf))

	files := unpack(t, buf.Bytes())
	assert.Contains(t, files, "core-pi.log")
	assert.Contains(t, files, "telegram.log")
	assert.Contains(t, files, "manifest.json")
	assert.Equal(t,
		"1970-01-01T00:00:01.000Z INFO     hannah.nlu: intent TurnOn\n"+
			"1970-01-01T00:00:02.000Z INFO     hannah.stt: mach das Licht an\n",
		files["core-pi.log"])

	var m Manifest
	require.NoError(t, json.Unmarshal([]byte(files["manifest.json"]), &m))
	require.Len(t, m.Sources, 2)
	coreSrc := m.Sources[0]
	assert.Equal(t, "core", coreSrc.Component)
	assert.Equal(t, "0.86.0", coreSrc.Version)
	assert.Equal(t, int64(2), coreSrc.Entries)
	require.Len(t, coreSrc.Gaps, 1)
	assert.Equal(t, int64(42), coreSrc.Gaps[0].Dropped)
}

func TestBuildExcludesCategories(t *testing.T) {
	st, dir := setup(t)
	var buf bytes.Buffer
	require.NoError(t, Build(context.Background(), st, store.Filter{
		ExcludeCategories: []int32{transcript},
	}, time.UnixMilli(10_000), dir, &buf))

	files := unpack(t, buf.Bytes())
	assert.NotContains(t, files["core-pi.log"], "mach das Licht an")
	assert.Contains(t, files["core-pi.log"], "intent TurnOn")

	var m Manifest
	require.NoError(t, json.Unmarshal([]byte(files["manifest.json"]), &m))
	assert.Equal(t, []string{"TRANSCRIPT"}, m.ExcludedCategories)
}

func TestBuildFiltersComponentsAndLeavesNoTempFiles(t *testing.T) {
	st, dir := setup(t)
	var buf bytes.Buffer
	require.NoError(t, Build(context.Background(), st, store.Filter{
		Components: []string{"telegram"},
	}, time.UnixMilli(10_000), dir, &buf))

	files := unpack(t, buf.Bytes())
	assert.NotContains(t, files, "core-pi.log")
	assert.Contains(t, files, "telegram.log")

	leftovers, err := filepath.Glob(filepath.Join(dir, "export-*"))
	require.NoError(t, err)
	assert.Empty(t, leftovers)
	_, err = os.Stat(filepath.Join(dir, "logs.db"))
	assert.NoError(t, err)
}

func TestFileNameIsSanitized(t *testing.T) {
	assert.Equal(t, "core-pi.log", fileName("core", "pi"))
	assert.Equal(t, "core.log", fileName("core", ""))
	assert.Equal(t, "we_ird-in_st.log", fileName("we/ird", "in st"))
}
