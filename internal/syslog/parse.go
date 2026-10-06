// Package syslog receives RFC 5424 syslog over UDP, the format the ESP satellites send, and
// stores the lines like those of every other component.
package syslog

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrMalformed is returned for a datagram that is not an RFC 5424 message.
var ErrMalformed = errors.New("malformed syslog message")

// Message is the part of an RFC 5424 message the collector keeps.
type Message struct {
	Severity int       // 0 (emergency) .. 7 (debug)
	Time     time.Time // zero if the sender gave none ("-") or none that parses
	Hostname string    // empty if the sender gave none ("-")
	AppName  string    // empty if the sender gave none ("-")
	Text     string
}

// Parse reads one RFC 5424 message:
//
//	<PRI>VERSION SP TIMESTAMP SP HOSTNAME SP APP-NAME SP PROCID SP MSGID SP STRUCTURED-DATA [SP MSG]
//
// A timestamp that doesn't parse counts as missing, so the line is kept instead of dropped.
// Structured data is skipped. A UTF-8 byte order mark in front of the text is removed, and so
// is the line break at its end.
func Parse(packet []byte) (Message, error) {
	s := string(packet)
	if !strings.HasPrefix(s, "<") {
		return Message{}, ErrMalformed
	}
	end := strings.IndexByte(s, '>')
	if end < 2 || end > 4 {
		return Message{}, ErrMalformed
	}
	pri, err := strconv.Atoi(s[1:end])
	if err != nil || pri < 0 || pri > 191 {
		return Message{}, ErrMalformed
	}
	rest := s[end+1:]

	// VERSION TIMESTAMP HOSTNAME APP-NAME PROCID MSGID
	var header [6]string
	for i := range header {
		field, remainder, ok := strings.Cut(rest, " ")
		if !ok {
			return Message{}, ErrMalformed
		}
		header[i], rest = field, remainder
	}
	if header[0] != "1" {
		return Message{}, ErrMalformed
	}

	rest, err = skipStructuredData(rest)
	if err != nil {
		return Message{}, err
	}
	text := ""
	if rest != "" {
		if rest[0] != ' ' {
			return Message{}, ErrMalformed
		}
		text = strings.TrimPrefix(rest[1:], "\xef\xbb\xbf")
	}

	msg := Message{
		Severity: pri % 8,
		Hostname: nilValue(header[2]),
		AppName:  nilValue(header[3]),
		Text:     strings.TrimRight(text, "\r\n"),
	}
	if header[1] != "-" {
		if t, err := time.Parse(time.RFC3339Nano, header[1]); err == nil {
			msg.Time = t
		}
	}
	return msg, nil
}

func nilValue(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// skipStructuredData returns what follows the STRUCTURED-DATA field: "-", or one or more
// [elements] in which a backslash escapes the next character and quotes may hold a "]".
func skipStructuredData(s string) (string, error) {
	if strings.HasPrefix(s, "-") {
		return s[1:], nil
	}
	pos := 0
	for pos < len(s) && s[pos] == '[' {
		pos++
		inQuote, closed := false, false
		for pos < len(s) && !closed {
			switch c := s[pos]; {
			case c == '\\' && pos+1 < len(s):
				pos++ // skip the escaped character
			case c == '"':
				inQuote = !inQuote
			case c == ']' && !inQuote:
				closed = true
			}
			pos++
		}
		if !closed {
			return "", ErrMalformed
		}
	}
	if pos == 0 {
		return "", ErrMalformed
	}
	return s[pos:], nil
}
