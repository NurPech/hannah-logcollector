package forward

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	pb "github.com/NurPech/hannah-proto-go/v5/hannahv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/syslog"
)

var satellite = store.Source{ID: 1, Component: "hannah-esp", Instance: "e072a1d01adc"}

func entry(level pb.LogLevel, message string) store.Entry {
	return store.Entry{
		SourceID:    1,
		TimestampMs: time.Date(2026, 10, 6, 12, 30, 5, 250_000_000, time.UTC).UnixMilli(),
		Level:       int32(level),
		Message:     message,
		Category:    int32(pb.LogCategory_LOG_CATEGORY_GENERAL),
	}
}

func TestFormat(t *testing.T) {
	got := string(Format(entry(pb.LogLevel_LOG_LEVEL_INFO, "I (12345) wifi: connected"), satellite))
	assert.Equal(t,
		`<134>1 2026-10-06T12:30:05.250Z e072a1d01adc hannah-esp - - [hannah@32473 category="GENERAL"] I (12345) wifi: connected`,
		got)
}

func TestFormatIsReadBackByTheReceiver(t *testing.T) {
	e := entry(pb.LogLevel_LOG_LEVEL_ERROR, "boom: i2s failed")
	e.Logger = "hannah.stt"
	msg, err := syslog.Parse(Format(e, store.Source{Component: "core", Instance: "psrvhva01"}))
	require.NoError(t, err)

	assert.Equal(t, 3, msg.Severity)
	assert.Equal(t, "psrvhva01", msg.Hostname)
	assert.Equal(t, "core", msg.AppName)
	assert.Equal(t, "boom: i2s failed", msg.Text)
	assert.True(t, msg.Time.Equal(time.UnixMilli(e.TimestampMs)))
}

func TestSeverityOfTheLevels(t *testing.T) {
	for level, want := range map[pb.LogLevel]int{
		pb.LogLevel_LOG_LEVEL_CRITICAL:    2,
		pb.LogLevel_LOG_LEVEL_ERROR:       3,
		pb.LogLevel_LOG_LEVEL_WARNING:     4,
		pb.LogLevel_LOG_LEVEL_INFO:        6,
		pb.LogLevel_LOG_LEVEL_DEBUG:       7,
		pb.LogLevel_LOG_LEVEL_UNSPECIFIED: 6,
	} {
		assert.Equal(t, want, severity(int32(level)), level.String())
	}
}

func TestHostnameAndAppNameAreMadeUsable(t *testing.T) {
	assert.Equal(t, "my_host__", nameField("my host é", 255))
	assert.Equal(t, "-", nameField("", 255))
	assert.Equal(t, strings.Repeat("a", 48), nameField(strings.Repeat("a", 100), 48))
	// A name with a space would shift every field behind it.
	msg, err := syslog.Parse(Format(entry(pb.LogLevel_LOG_LEVEL_INFO, "x"), store.Source{Component: "my app", Instance: "my host"}))
	require.NoError(t, err)
	assert.Equal(t, "my_app", msg.AppName)
	assert.Equal(t, "my_host", msg.Hostname)
}

func TestStructuredDataEscapesWhatBreaksAParameter(t *testing.T) {
	e := entry(pb.LogLevel_LOG_LEVEL_INFO, "x")
	e.Logger = `a"b]c\d`
	line := string(Format(e, satellite))
	assert.Contains(t, line, `logger="a\"b\]c\\d"`)

	msg, err := syslog.Parse([]byte(line))
	require.NoError(t, err, "the escaped data is skipped by the receiver's parser")
	assert.Equal(t, "x", msg.Text)
}

func TestNoStructuredDataWithoutCategoryAndLogger(t *testing.T) {
	line := string(Format(store.Entry{TimestampMs: 0, Message: "x"}, satellite))
	assert.Contains(t, line, " - - - x", "PROCID, MSGID and the structured data are all nil")
}

func TestTrailingLineBreakIsRemovedAndInnerOnesStay(t *testing.T) {
	line := string(Format(entry(pb.LogLevel_LOG_LEVEL_ERROR, "Traceback:\n  File x\n"), satellite))
	assert.True(t, strings.HasSuffix(line, "Traceback:\n  File x"))
}

func TestLongMessageIsCutAtACharacterBoundary(t *testing.T) {
	long := strings.Repeat("ä", 10000) // two bytes each
	line := Format(entry(pb.LogLevel_LOG_LEVEL_INFO, long), satellite)

	assert.LessOrEqual(t, len(line), 8192, "below what Alloy accepts by default")
	assert.True(t, utf8.Valid(line))
	assert.True(t, strings.HasSuffix(string(line), truncated))
}

func TestFrameCountsOctets(t *testing.T) {
	assert.Equal(t, "3 abc", string(Frame([]byte("abc"))))
	assert.Equal(t, "5 ä\n\nx", string(Frame([]byte("ä\n\nx"))), "the count is in bytes and a line break is fine")
}
