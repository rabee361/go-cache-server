package main

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// trafficLog is the shared logger for client<->server communication.
// It stays nil unless enableTrafficLogging is called, so the test binary
// (which calls serve()/handleConn() directly, never main()) prints nothing.
var trafficLog *log.Logger

// enableTrafficLogging turns on per-command traffic logging to stdout.
// log.Logger is safe for concurrent use, so many connection goroutines can
// share one logger without their lines interleaving mid-message.
func enableTrafficLogging() {
	trafficLog = log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)
}

// logComm writes a traffic line when logging is enabled; otherwise it's a no-op.
func logComm(format string, args ...any) {
	if trafficLog != nil {
		trafficLog.Printf(format, args...)
	}
}

const (
	logMaxTokenLen   = 64 // cap on any single logged token (command name, arg, value)
	logMaxArrayElems = 8  // cap on how many elements of a collection reply we expand
	logMaxDepth      = 2  // how deep we recurse into nested arrays/maps
)

// sanitize collapses a raw token into a single readable line: newlines become
// spaces and anything over logMaxTokenLen bytes is cut with a suffix showing
// the full length. Keeps 1MB Celery payloads from flooding the terminal.
func sanitize(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
	if len(s) > logMaxTokenLen {
		return fmt.Sprintf("%s...(%dB)", s[:logMaxTokenLen], len(s))
	}
	return s
}

// quoteIfNeeded wraps a token in %q only when it contains whitespace, so that
// space-separated command args stay unambiguous on one line. Single logical
// values (errors, simple strings) are passed through sanitize untouched.
func quoteIfNeeded(s string) string {
	s = sanitize(s)
	if strings.ContainsAny(s, " \t") {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// argsSummary renders an inbound command ("SET", "foo", "bar") as one line.
func argsSummary(args []string) string {
	tokens := make([]string, 0, len(args))
	for i, a := range args {
		if i == logMaxArrayElems {
			tokens = append(tokens, fmt.Sprintf("...(+%d args)", len(args)-i))
			break
		}
		tokens = append(tokens, quoteIfNeeded(a))
	}
	return strings.Join(tokens, " ")
}

// replySummary renders an outbound Reply as one line.
func replySummary(r Reply) string {
	return replySummaryDepth(r, 0)
}

func replySummaryDepth(r Reply, depth int) string {
	switch r.Kind {
	case '+':
		return "+" + sanitize(r.Str)
	case '-':
		return "-" + sanitize(r.Str)
	case ':':
		return fmt.Sprintf(":%d", r.Int)
	case '$':
		if r.Null {
			return "$-1 (nil)"
		}
		return fmt.Sprintf("$%d %s", len(r.Str), sanitize(r.Str))
	case '*':
		if r.Null {
			return "*-1 (nil)"
		}
		return collectionSummary(fmt.Sprintf("*%d", len(r.Arr)), r.Arr, depth)
	case '%':
		return collectionSummary(fmt.Sprintf("%%%d", len(r.Map)/2), r.Map, depth)
	default:
		return fmt.Sprintf("?%c", r.Kind)
	}
}

// collectionSummary expands a few elements of an array/map, then elides the rest.
func collectionSummary(prefix string, elems []Reply, depth int) string {
	if depth >= logMaxDepth || len(elems) == 0 {
		return fmt.Sprintf("%s [...]", prefix)
	}
	shown := len(elems)
	elided := ""
	if shown > logMaxArrayElems {
		shown = logMaxArrayElems
		elided = fmt.Sprintf(" ...(+%d)", len(elems)-shown)
	}
	parts := make([]string, 0, shown)
	for _, e := range elems[:shown] {
		parts = append(parts, replySummaryDepth(e, depth+1))
	}
	return fmt.Sprintf("%s [%s%s]", prefix, strings.Join(parts, ", "), elided)
}
