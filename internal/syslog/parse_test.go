package syslog

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseSatellitePacket(t *testing.T) {
	// What the satellite firmware sends: no timestamp, device ID as hostname, app name hannah-esp.
	msg, err := Parse([]byte("<134>1 - e072a1d01adc hannah-esp - - - I (12345) wifi: connected"))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Severity != 6 || msg.Hostname != "e072a1d01adc" || msg.AppName != "hannah-esp" ||
		msg.Text != "I (12345) wifi: connected" || !msg.Time.IsZero() {
		t.Fatalf("got %+v", msg)
	}
}

func TestParseTimestampAndTextDetails(t *testing.T) {
	for _, tc := range []struct {
		name, packet string
		want         Message
	}{
		{
			"timestamp",
			"<11>1 2026-10-06T14:30:05.250+02:00 host app 42 ID1 - boom",
			Message{Severity: 3, Time: time.Date(2026, 10, 6, 12, 30, 5, 250_000_000, time.UTC), Hostname: "host", AppName: "app", Text: "boom"},
		},
		{
			"timestamp that does not parse counts as missing",
			"<14>1 yesterday host app - - - x",
			Message{Severity: 6, Hostname: "host", AppName: "app", Text: "x"},
		},
		{
			"nil hostname and app name",
			"<14>1 - - - - - - x",
			Message{Severity: 6, Text: "x"},
		},
		{
			"structured data is skipped, also with escaped brackets and quotes",
			`<14>1 - h a - - [x@1 k="a]b" j="q\"]"][y@2 z="1"] the text`,
			Message{Severity: 6, Hostname: "h", AppName: "a", Text: "the text"},
		},
		{
			"byte order mark and line break are removed",
			"<14>1 - h a - - - \xef\xbb\xbfhello\r\n",
			Message{Severity: 6, Hostname: "h", AppName: "a", Text: "hello"},
		},
		{
			"no text at all",
			"<14>1 - h a - - -",
			Message{Severity: 6, Hostname: "h", AppName: "a"},
		},
		{
			"a message with spaces and empty text parts",
			"<14>1 - h a - - -  two  spaces ",
			Message{Severity: 6, Hostname: "h", AppName: "a", Text: " two  spaces "},
		},
		{
			"facility does not matter for the severity",
			"<191>1 - h a - - - x",
			Message{Severity: 7, Hostname: "h", AppName: "a", Text: "x"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]byte(tc.packet))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Time.Equal(tc.want.Time) {
				t.Errorf("time %v, want %v", got.Time, tc.want.Time)
			}
			got.Time, tc.want.Time = time.Time{}, time.Time{}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for name, packet := range map[string]string{
		"empty":                    "",
		"plain text":               "just some log line",
		"no priority":              "1 - h a - - - x",
		"priority not a number":    "<abc>1 - h a - - - x",
		"priority out of range":    "<192>1 - h a - - - x",
		"priority too long":        "<00000>1 - h a - - - x",
		"unterminated priority":    "<14 1 - h a - - - x",
		"old BSD syslog (RFC3164)": "<34>Oct 11 22:14:15 mymachine su: 'su root' failed",
		"wrong version":            "<14>2 - h a - - - x",
		"too few header fields":    "<14>1 - h a",
		"no structured data":       "<14>1 - h a - - x",
		"unterminated element":     `<14>1 - h a - - [x@1 k="v" text`,
		"text glued to the data":   "<14>1 - h a - - -x",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(packet)); !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
}

func TestParseVeryLongText(t *testing.T) {
	text := strings.Repeat("x", 60000)
	msg, err := Parse([]byte("<14>1 - h a - - - " + text))
	if err != nil || msg.Text != text {
		t.Fatalf("err %v, text length %d", err, len(msg.Text))
	}
}
