package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	maxBulkLen = 1 << 20 // refuse bulk strings larger than 1 MB (DoS guard)
	maxArgs    = 1024    // refuse commands with more than 1024 arguments
)

// Reply is a RESP reply value. Kind is one of '+', '-', ':', '$', '*'.
type Reply struct {
	Kind byte     // '+', '-', ':', '$', '*'
	Str  string   // payload for '+', '-', '$'
	Int  int64    // payload for ':'
	Null bool     // true -> "$-1\r\n"
	Arr  []Reply  // payload for '*'; this subset never emits arrays
}

func simpleString(s string) Reply { return Reply{Kind: '+', Str: s} }
func errReply(msg string) Reply   { return Reply{Kind: '-', Str: msg} }
func intReply(n int64) Reply      { return Reply{Kind: ':', Int: n} }
func bulkReply(b []byte) Reply    { return Reply{Kind: '$', Str: string(b)} }
func nullReply() Reply            { return Reply{Kind: '$', Null: true} }

// writeReply serializes reader into writer in RESP format. It does NOT flush; flushing
// is the connection loop's job so batching stays possible.
func writeReply(writer *bufio.Writer, reader Reply) error {
	switch reader.Kind {
	case '+', '-':
		msg := strings.ReplaceAll(strings.ReplaceAll(reader.Str, "\r", " "), "\n", " ")
		if _, err := fmt.Fprintf(writer, "%c%s\r\n", reader.Kind, msg); err != nil {
			return err
		}
	case ':':
		if _, err := fmt.Fprintf(writer, ":%d\r\n", reader.Int); err != nil {
			return err
		}
	case '$':
		if reader.Null {
			if _, err := writer.WriteString("$-1\r\n"); err != nil {
				return err
			}
			return nil
		}
		if _, err := fmt.Fprintf(writer, "$%d\r\n", len(reader.Str)); err != nil {
			return err
		}
		if _, err := writer.WriteString(reader.Str); err != nil {
			return err
		}
		if _, err := writer.WriteString("\r\n"); err != nil {
			return err
		}
	case '*':
		if _, err := fmt.Fprintf(writer, "*%d\r\n", len(reader.Arr)); err != nil {
			return err
		}
		for _, elem := range reader.Arr {
			if err := writeReply(writer, elem); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown reply kind %q", reader.Kind)
	}
	return nil
}

// readCommand reads one complete command from r. It accepts the RESP array
// form (*<n> followed by n bulk strings) and the inline form (a plain text
// line split on whitespace). It returns io.EOF when the connection was closed
// cleanly before any bytes of a new command arrived; any other error is a
// protocol violation.
func readCommand(reader *bufio.Reader) ([]string, error) {
	first, err := reader.Peek(1)
	if err != nil {
		return nil, err
	}
	if first[0] != '*' {
		return readInline(reader)
	}
	return readArray(reader)
}

func readInline(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return nil, errors.New("truncated inline command")
		}
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	return strings.Fields(line), nil
}

func readArray(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return nil, errors.New("truncated array header")
		}
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) < 2 || line[0] != '*' {
		return nil, fmt.Errorf("expected '*', got %q", line)
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil || n < 1 {
		return nil, fmt.Errorf("invalid multibulk length %q", line[1:])
	}
	if n > maxArgs {
		return nil, fmt.Errorf("too many arguments: %d", n)
	}

	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		arg, err := readBulk(reader)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}
	return args, nil
}

func readBulk(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return "", errors.New("truncated bulk header")
		}
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) < 2 || line[0] != '$' {
		return "", fmt.Errorf("expected '$', got %q", line)
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil || n < 0 {
		return "", fmt.Errorf("invalid bulk length %q", line[1:])
	}
	if n > maxBulkLen {
		return "", fmt.Errorf("invalid bulk length %d (maximum %d)", n, maxBulkLen)
	}

	buf := make([]byte, n+2)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", err
	}
	if buf[n] != '\r' || buf[n+1] != '\n' {
		return "", errors.New("invalid bulk string terminator")
	}
	return string(buf[:n]), nil
}
