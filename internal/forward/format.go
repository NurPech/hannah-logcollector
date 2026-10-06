package forward

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/NurPech/hannah-proto-go/v5/hannahv2"

	"dev.kernstock.net/gessinger/voice/hannah-logcollector/internal/store"
)

const (
	// facilityLocal0 is the syslog facility of the forwarded lines (the satellites use it as well).
	facilityLocal0 = 16

	// maxText keeps a whole line below the 8192 bytes Alloy's syslog listener accepts by default
	// (max_message_length), header and structured data included.
	maxText = 7000

	maxHostname = 255
	maxAppName  = 48

	// sdID names the structured data element carrying category and logger. The enterprise
	// number is the one RFC 5612 reserves for documentation: Hannah has none of its own.
	sdID = "hannah@32473"

	timeLayout = "2006-01-02T15:04:05.000Z"

	truncated = "..."
)

// Format renders an entry as one RFC 5424 message, without framing:
//
//	<PRI>1 TIMESTAMP HOSTNAME APP-NAME - - [hannah@32473 category="…" logger="…"] MSG
//
// The timestamp is the entry's own, the hostname the source's instance and the app name its
// component, so the receiver can label by them. The severity comes from the level, category
// and logger go into the structured data. A message that is too long is cut.
func Format(e store.Entry, src store.Source) []byte {
	var b strings.Builder
	b.WriteByte('<')
	b.WriteString(strconv.Itoa(facilityLocal0*8 + severity(e.Level)))
	b.WriteString(">1 ")
	b.WriteString(time.UnixMilli(e.TimestampMs).UTC().Format(timeLayout))
	b.WriteByte(' ')
	b.WriteString(nameField(src.Instance, maxHostname))
	b.WriteByte(' ')
	b.WriteString(nameField(src.Component, maxAppName))
	b.WriteString(" - - ")
	b.WriteString(structuredData(e))
	b.WriteByte(' ')
	b.WriteString(text(e.Message))
	return []byte(b.String())
}

// Frame prepends the octet count of RFC 6587, how a stream of syslog messages over TCP is
// told apart without relying on a line break (a message may hold one).
func Frame(message []byte) []byte {
	prefix := strconv.Itoa(len(message)) + " "
	out := make([]byte, 0, len(prefix)+len(message))
	return append(append(out, prefix...), message...)
}

// severity maps the level of the shipping API to a syslog severity.
func severity(level int32) int {
	switch pb.LogLevel(level) {
	case pb.LogLevel_LOG_LEVEL_CRITICAL:
		return 2
	case pb.LogLevel_LOG_LEVEL_ERROR:
		return 3
	case pb.LogLevel_LOG_LEVEL_WARNING:
		return 4
	case pb.LogLevel_LOG_LEVEL_DEBUG:
		return 7
	default: // info, and a line that didn't say
		return 6
	}
}

// nameField makes s usable as HOSTNAME or APP-NAME: printable US-ASCII without space, at most
// limit long, "-" if nothing is left.
func nameField(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= limit {
			break
		}
		if r > ' ' && r < 0x7f {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

func structuredData(e store.Entry) string {
	category := strings.TrimPrefix(pb.LogCategory(e.Category).String(), "LOG_CATEGORY_")
	hasCategory := pb.LogCategory(e.Category) != pb.LogCategory_LOG_CATEGORY_UNSPECIFIED
	if !hasCategory && e.Logger == "" {
		return "-"
	}
	var b strings.Builder
	b.WriteString("[" + sdID)
	if hasCategory {
		b.WriteString(` category="` + escapeParam(category) + `"`)
	}
	if e.Logger != "" {
		b.WriteString(` logger="` + escapeParam(e.Logger) + `"`)
	}
	b.WriteByte(']')
	return b.String()
}

// escapeParam escapes what ends or breaks a structured data parameter value.
func escapeParam(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `]`, `\]`).Replace(s)
}

// text is the message without its trailing line break, cut to maxText bytes at a character boundary.
func text(message string) string {
	message = strings.TrimRight(message, "\r\n")
	if len(message) <= maxText {
		return message
	}
	cut := maxText - len(truncated)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + truncated
}
