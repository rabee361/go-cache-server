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
	"time"
)

func startTestServer(t *testing.T, store *MemoryStore) net.Addr {
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

// readResp reads one RESP reply (recursively for arrays) and returns its raw
// wire bytes.
func readResp(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read reply line: %v", err)
	}
	switch line[0] {
	case '$':
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
	case '*':
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(line[1:], "\n"), "\r"))
		if err != nil {
			t.Fatalf("bad array length %q: %v", line, err)
		}
		out := line
		for i := 0; i < n; i++ {
			out += readResp(t, r)
		}
		return out
	case '%':
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(line[1:], "\n"), "\r"))
		if err != nil {
			t.Fatalf("bad map length %q: %v", line, err)
		}
		out := line
		for i := 0; i < n*2; i++ {
			out += readResp(t, r)
		}
		return out
	case '_':
		return line
	default:
		return line
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

func TestE2EDel(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("*3\r\n$3\r\nSET\r\n$1\r\na\r\n$1\r\n1\r\n*3\r\n$3\r\nSET\r\n$1\r\nb\r\n$1\r\n2\r\n"))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("first SET reply = %q, want %q", got, "+OK\r\n")
	}
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("second SET reply = %q, want %q", got, "+OK\r\n")
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
	sendBytes(t, conn, []byte("GET foo\r\n"))
	if got := readResp(t, r); got != "$-1\r\n" {
		t.Errorf("GET after error = %q, want %q", got, "$-1\r\n")
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
		t.Fatalf("SET reply = %q, want %q", got, "+OK\r\n")
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
		t.Fatalf("SET reply = %q, want %q", got, "+OK\r\n")
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
	sendBytes(t, conn, []byte("*3\r\n$3\r\nSET\r\n$1\r\na\r\n$1\r\n1\r\n*3\r\n$3\r\nSET\r\n$1\r\nb\r\n$1\r\n2\r\n"))
	for i, want := range []string{"+OK\r\n", "+OK\r\n"} {
		if got := readResp(t, r); got != want {
			t.Errorf("reply %d = %q, want %q", i, got, want)
		}
	}
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$1\r\na\r\n*2\r\n$3\r\nGET\r\n$1\r\nb\r\n"))
	for i, want := range []string{"$1\r\n1\r\n", "$1\r\n2\r\n"} {
		if got := readResp(t, r); got != want {
			t.Errorf("reply %d = %q, want %q", i, got, want)
		}
	}
}

// Protocol errors close the connection silently, without an error reply.
func TestE2EProtocolError(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("*1\r\n$4\r\nPINGxx\r\n"))
	line, err := r.ReadString('\n')
	if line != "" || err != io.EOF {
		t.Errorf("after protocol error: got %q, %v; want empty line and io.EOF", line, err)
	}
}

func TestE2ESetExpiry(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("*5\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n$2\r\nPX\r\n$3\r\n200\r\n"))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("SET PX reply = %q, want %q", got, "+OK\r\n")
	}
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$1\r\nk\r\n"))
	if got := readResp(t, r); got != "$1\r\nv\r\n" {
		t.Errorf("GET before expiry = %q, want %q", got, "$1\r\nv\r\n")
	}
	time.Sleep(400 * time.Millisecond)
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$1\r\nk\r\n"))
	if got := readResp(t, r); got != "$-1\r\n" {
		t.Errorf("GET after expiry = %q, want %q", got, "$-1\r\n")
	}
}

func TestE2ESetEx(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("*5\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n$2\r\nEX\r\n$3\r\n100\r\n"))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("SET EX reply = %q, want %q", got, "+OK\r\n")
	}
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$1\r\nk\r\n"))
	if got := readResp(t, r); got != "$1\r\nv\r\n" {
		t.Errorf("GET = %q, want %q", got, "$1\r\nv\r\n")
	}
	sendBytes(t, conn, []byte("*7\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n$2\r\nEX\r\n$3\r\n100\r\n$2\r\nPX\r\n$3\r\n200\r\n"))
	if got := readResp(t, r); got != "-ERR syntax error\r\n" {
		t.Errorf("SET EX+PX = %q, want %q", got, "-ERR syntax error\r\n")
	}
	sendBytes(t, conn, []byte("*4\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n$2\r\nEX\r\n"))
	if got := readResp(t, r); got != "-ERR syntax error\r\n" {
		t.Errorf("SET EX without value = %q, want %q", got, "-ERR syntax error\r\n")
	}
}

func TestE2EClientSetinfo(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("CLIENT SETINFO LIB-NAME redis-py\r\n"))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("CLIENT SETINFO reply = %q, want %q", got, "+OK\r\n")
	}
	sendBytes(t, conn, []byte("CLIENT SETINFO LIB-VER 5.2.1\r\n"))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("CLIENT SETINFO reply = %q, want %q", got, "+OK\r\n")
	}
	// the handshake must not break the connection
	sendBytes(t, conn, []byte("GET foo\r\n"))
	if got := readResp(t, r); got != "$-1\r\n" {
		t.Errorf("GET after SETINFO = %q, want %q", got, "$-1\r\n")
	}
}

func TestE2EHello(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)

	// HELLO 3 switches the connection to RESP3; the reply is a map (%7). The
	// connection id is an integer we cannot predict, so assert around it.
	sendBytes(t, conn, []byte("*2\r\n$5\r\nHELLO\r\n$1\r\n3\r\n"))
	got := readResp(t, r)
	wantPrefix := "%7\r\n$6\r\nserver\r\n$12\r\ncache-server\r\n$7\r\nversion\r\n$5\r\n0.1.0\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:"
	wantSuffix := "\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
	if !strings.HasPrefix(got, wantPrefix) || !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("HELLO 3 reply = %q, want a RESP3 map with server fields", got)
	}

	// a missing GET now replies with the RESP3 null
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$7\r\nmissing\r\n"))
	if got := readResp(t, r); got != "_\r\n" {
		t.Errorf("GET missing in RESP3 = %q, want %q", got, "_\r\n")
	}

	// SET/GET still round-trip in RESP3
	sendBytes(t, conn, []byte("*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n"))
	if got := readResp(t, r); got != "+OK\r\n" {
		t.Fatalf("SET in RESP3 = %q, want %q", got, "+OK\r\n")
	}
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$1\r\nk\r\n"))
	if got := readResp(t, r); got != "$1\r\nv\r\n" {
		t.Errorf("GET in RESP3 = %q, want %q", got, "$1\r\nv\r\n")
	}

	// bare HELLO replies in the current protocol (still RESP3)
	sendBytes(t, conn, []byte("*1\r\n$5\r\nHELLO\r\n"))
	if got := readResp(t, r); !strings.HasPrefix(got, "%7\r\n") {
		t.Errorf("bare HELLO in RESP3 = %q, want a map", got)
	}

	// HELLO 2 switches back to RESP2: flat array reply, nulls return to $-1
	sendBytes(t, conn, []byte("*2\r\n$5\r\nHELLO\r\n$1\r\n2\r\n"))
	got = readResp(t, r)
	if !strings.HasPrefix(got, "*14\r\n") || !strings.Contains(got, "$5\r\nproto\r\n:2\r\n") {
		t.Errorf("HELLO 2 reply = %q, want a flat array with proto 2", got)
	}
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$7\r\nmissing\r\n"))
	if got := readResp(t, r); got != "$-1\r\n" {
		t.Errorf("GET missing in RESP2 = %q, want %q", got, "$-1\r\n")
	}

	// bad protocol version: Redis's exact error, connection stays alive
	sendBytes(t, conn, []byte("*2\r\n$5\r\nHELLO\r\n$1\r\n4\r\n"))
	if got := readResp(t, r); got != "-NOPROTO unsupported protocol version\r\n" {
		t.Errorf("HELLO 4 = %q, want %q", got, "-NOPROTO unsupported protocol version\r\n")
	}
	sendBytes(t, conn, []byte("*2\r\n$3\r\nGET\r\n$1\r\nk\r\n"))
	if got := readResp(t, r); got != "$1\r\nv\r\n" {
		t.Errorf("GET after HELLO 4 = %q, want %q", got, "$1\r\nv\r\n")
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
