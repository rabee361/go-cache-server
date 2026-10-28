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
	maxBulkLen = 1048576
	maxArgs    = 1024
)

type Reply struct {
	Kind byte
	Str  string
	Int  int64
	Null bool
	Arr  []Reply
	Map  []Reply // alternating key/value pairs; encoded per protocol ('%' or flat '*')
}

func simpleString(s string) Reply { return Reply{Kind: '+', Str: s} }
func errReply(msg string) Reply   { return Reply{Kind: '-', Str: msg} }
func intReply(n int64) Reply      { return Reply{Kind: ':', Int: n} }
func bulkReply(b []byte) Reply    { return Reply{Kind: '$', Str: string(b)} }
func nullReply() Reply            { return Reply{Kind: '$', Null: true} }
func arrayReply(elems []Reply) Reply {
	return Reply{Kind: '*', Arr: elems}
}
func mapReply(entries []Reply) Reply {
	return Reply{Kind: '%', Map: entries}
}

func writeReply(writer *bufio.Writer, proto int, reader Reply) error {
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
			if proto >= 3 {
				_, err := writer.WriteString("_\r\n")
				return err
			}
			_, err := writer.WriteString("$-1\r\n")
			return err
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
			if err := writeReply(writer, proto, elem); err != nil {
				return err
			}
		}
	case '%':
		if proto >= 3 {
			if _, err := fmt.Fprintf(writer, "%%%d\r\n", len(reader.Map)/2); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(writer, "*%d\r\n", len(reader.Map)); err != nil {
				return err
			}
		}
		for _, elem := range reader.Map {
			if err := writeReply(writer, proto, elem); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown reply kind %q", reader.Kind)
	}
	return nil
}

func readCommand(reader *bufio.Reader) ([]string, error) {
	first, err := reader.Peek(1)
	if err != nil {
		return nil, err
	}
	if first[0] != '*' {
		fmt.Println("Inline command received", first[0])
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
