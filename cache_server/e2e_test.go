package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func startTestServer(t *testing.T, store Store) net.Addr {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go serve(ln, store)
	t.Cleanup(func() { ln.Close() })
	return ln.Addr()
}

func dialServer(t *testing.T, addr net.Addr) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, bufio.NewReader(conn)
}

func sendBytes(t *testing.T, conn net.Conn, b []byte) {
	t.Helper()
	if _, err := conn.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readResp reads one RESP reply and returns its raw wire bytes.
func readResp(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read reply line: %v", err)
	}
	if !strings.HasPrefix(line, "$") {
		return line
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(line[1:], "\n"), "\r"))
	if err != nil {
		t.Fatalf("bad bulk length %q: %v", line, err)
	}
	if n == -1 {
		return line
	}
	rest := make([]byte, n+2)
	if _, err := io.ReadFull(r, rest); err != nil {
		t.Fatalf("read bulk payload: %v", err)
	}
	return line + string(rest)
}

func TestE2EPing(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("*1\r\n$4\r\nPING\r\n"))
	if got := readResp(t, r); got != "+PONG\r\n" {
		t.Errorf("got %q, want %q", got, "+PONG\r\n")
	}
}

func TestE2ESetGetRoundTrip(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n"))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("SET reply = %q, want %q", got, "+OK\r\n")
	}
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$3\r\nfoo\r\n"))
	if got := readResp(t, r); got != "$3\r\nbar\r\n" {
		t.Errorf("GET reply = %q, want %q", got, "$3\r\nbar\r\n")
	}
}

func TestE2EGetMissing(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$4\r\nnope\r\n"))
	if got := readResp(t, r); got != "$-1\r\n" {
		t.Errorf("got %q, want %q", got, "$-1\r\n")
	}
}

func TestE2ECounts(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("*3\r\n$3\r\nSET\r\n$1\r\na\r\n$1\r\n1\r\n*3\r\n$3\r\nSET\r\n$1\r\nb\r\n$1\r\n2\r\n"))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("first SET reply = %q", got)
	}
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("second SET reply = %q", got)
	}
	sendBytes(t, conn, []byte("*4\r\n$6\r\nEXISTS\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n"))
	if got := readResp(t, r); got != ":2\r\n" {
		t.Errorf("EXISTS reply = %q, want %q", got, ":2\r\n")
	}
	sendBytes(t, conn, []byte("*4\r\n$3\r\nDEL\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n"))
	if got := readResp(t, r); got != ":2\r\n" {
		t.Errorf("DEL reply = %q, want %q", got, ":2\r\n")
	}
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$1\r\na\r\n"))
	if got := readResp(t, r); got != "$-1\r\n" {
		t.Errorf("GET after DEL reply = %q, want %q", got, "$-1\r\n")
	}
}

func TestE2EUnknownCommand(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("FOO bar\r\n"))
	if got := readResp(t, r); got != "-ERR unknown command 'FOO'\r\n" {
		t.Errorf("got %q, want %q", got, "-ERR unknown command 'FOO'\r\n")
	}
	// command errors must not close the connection
	sendBytes(t, conn, []byte("PING\r\n"))
	if got := readResp(t, r); got != "+PONG\r\n" {
		t.Errorf("PING after error = %q, want %q", got, "+PONG\r\n")
	}
}

func TestE2EWrongArity(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("GET\r\n"))
	if got := readResp(t, r); got != "-ERR wrong number of arguments for 'GET' command\r\n" {
		t.Errorf("got %q, want %q", got, "-ERR wrong number of arguments for 'GET' command\r\n")
	}
}

func TestE2EInline(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("SET foo bar\r\n"))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("SET reply = %q", got)
	}
	sendBytes(t, conn, []byte("GET foo\r\n"))
	if got := readResp(t, r); got != "$3\r\nbar\r\n" {
		t.Errorf("GET reply = %q, want %q", got, "$3\r\nbar\r\n")
	}
}

func TestE2EBinaryValue(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	value := []byte("line1\r\nline2\x00\xff")
	cmd := fmt.Sprintf("*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$%d\r\n", len(value))
	sendBytes(t, conn, append(append([]byte(cmd), value...), []byte("\r\n")...))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("SET reply = %q", got)
	}
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$1\r\nk\r\n"))
	want := fmt.Sprintf("$%d\r\n%s\r\n", len(value), value)
	if got := readResp(t, r); got != want {
		t.Errorf("GET reply = %q, want %q", got, want)
	}
}

func TestE2EPipelined(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	// both commands in a single write
	sendBytes(t, conn, []byte("*1\r\n$4\r\nPING\r\n*1\r\n$4\r\nPING\r\n"))
	for i := 0; i < 2; i++ {
		if got := readResp(t, r); got != "+PONG\r\n" {
			t.Errorf("reply %d = %q, want %q", i, got, "+PONG\r\n")
		}
	}
}

func TestE2EProtocolError(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("*1\r\n$4\r\nPINGxx\r\n"))
	got := readResp(t, r)
	if !strings.HasPrefix(got, "-ERR Protocol error:") {
		t.Errorf("got %q, want protocol error reply", got)
	}
}

func TestE2EQuit(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("QUIT\r\n"))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("QUIT reply = %q, want %q", got, "+OK\r\n")
	}
	line, err := r.ReadString('\n')
	if line != "" || err != io.EOF {
		t.Errorf("after QUIT: got %q, %v; want empty line and io.EOF", line, err)
	}
}

func TestE2EConcurrent(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	const numClients = 50
	var wg sync.WaitGroup
	wg.Add(numClients)

	for i := 0; i < numClients; i++ {
		go func(idx int) {
			defer wg.Done()
			conn, err := net.Dial("tcp", addr.String())
			if err != nil {
				t.Errorf("client %d: dial: %v", idx, err)
				return
			}
			defer conn.Close()
			r := bufio.NewReader(conn)

			key := fmt.Sprintf("key%d", idx)
			val := fmt.Sprintf("value%d", idx)
			fmt.Fprintf(conn, "*3\r\n$3\r\nSET\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", len(key), key, len(val), val)
			line, err := r.ReadString('\n')
			if err != nil || line != "+OK\r\n" {
				t.Errorf("client %d: SET reply = %q, %v", idx, line, err)
				return
			}

			fmt.Fprintf(conn, "*2\r\n$3\r\nGET\r\n$%d\r\n%s\r\n", len(key), key)
			want := fmt.Sprintf("$%d\r\n%s\r\n", len(val), val)
			got := make([]byte, 0, len(want))
			for len(got) < len(want) {
				line, err = r.ReadString('\n')
				if err != nil {
					t.Errorf("client %d: read GET reply: %v", idx, err)
					return
				}
				got = append(got, line...)
			}
			if string(got) != want {
				t.Errorf("client %d: GET reply = %q, want %q", idx, got, want)
			}
		}(i)
	}
	wg.Wait()
}
