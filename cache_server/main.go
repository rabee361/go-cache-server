package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"

	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	mu         sync.RWMutex
	maxMemory  int64 // bytes; 0 = unlimited
	maxClients int   // 0 must never happen; default
}

var serverConfig = &Config{maxClients: 1000}

func (c *Config) SetMaxMemory(n int64) { c.mu.Lock(); defer c.mu.Unlock(); c.maxMemory = n }
func (c *Config) MaxMemory() int64     { c.mu.RLock(); defer c.mu.RUnlock(); return c.maxMemory }

func (c *Config) SetMaxClients(n int) { c.mu.Lock(); defer c.mu.Unlock(); c.maxClients = n }
func (c *Config) MaxClients() int     { c.mu.RLock(); defer c.mu.RUnlock(); return c.maxClients }

var activeConns atomic.Int64

func serve(listener net.Listener, store *MemoryStore) error {
	hub := NewPubSub()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		if n := activeConns.Add(1); n > int64(serverConfig.MaxClients()) {
			activeConns.Add(-1)
			log.Printf("connection limit (%d) reached, rejecting %s", serverConfig.MaxClients(), conn.RemoteAddr())
			conn.SetWriteDeadline(time.Now().Add(time.Second))
			fmt.Fprintf(conn, "-ERR max number of clients reached\r\n")
			conn.Close()
			continue
		}
		go func() {
			defer activeConns.Add(-1)
			handleConn(conn, store, hub)
		}()
	}
}

func handleConn(conn net.Conn, store *MemoryStore, hub *PubSub) {
	defer conn.Close()
	defer func() {
		if p := recover(); p != nil {
			log.Printf("panic in connection handler: %v", p)
		}
	}()

	state := newClientState()
	state.hub = hub
	remote := conn.RemoteAddr().String()
	logComm("[conn %d] connected from %s", state.id, remote)
	defer logComm("[conn %d] disconnected (%s)", state.id, remote)

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	// Serialise socket writes: the read loop and any PUBLISH reaching this
	// subscriber from another goroutine can both write concurrently. closed
	// stops a publisher writing to a half-torn-down connection.
	var wmu sync.Mutex
	closed := false
	state.write = func(proto int, r Reply) {
		wmu.Lock()
		defer wmu.Unlock()
		if closed {
			return
		}
		logComm("[conn %d] <-- %s", state.id, replySummary(r))
		writeReply(writer, proto, r)
		writer.Flush()
	}
	defer func() {
		wmu.Lock()
		closed = true
		wmu.Unlock()
		hub.removeClient(state)
	}()

	for {
		args, err := readCommand(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			logComm("[conn %d] read error: %v", state.id, err)
			return
		}

		logComm("[conn %d] --> %s", state.id, argsSummary(args))
		reply := dispatch(store, state, args)
		if reply.Kind != 0 {
			state.write(state.protocol_version, reply)
		}
	}
}

func main() {
	addr := flag.String("addr", "localhost:6379", "listen address")
	maxClients := flag.Int("maxclients", 1000, "maximum simultaneous client connections")
	maxMemory := flag.String("maxmemory", "0", "memory budget for string values (bytes, or with unit: 100mb, 1gb); 0 = unlimited")
	enableLog := flag.Bool("log", true, "log client/server communication to stdout")
	flag.Parse()

	if *maxClients <= 0 {
		log.Fatal("-maxclients must be greater than 0")
	}
	mm, err := parseMemSize(*maxMemory)
	if err != nil {
		log.Fatalf("invalid -maxmemory %q: %v", *maxMemory, err)
	}
	serverConfig.SetMaxClients(*maxClients)
	serverConfig.SetMaxMemory(mm)
	if *enableLog {
		enablecommandLogging()
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", *addr, err)
	}
	log.Printf("RESP cache server listening on %s (maxclients=%d, maxmemory=%d)", *addr, *maxClients, mm)
	log.Fatal(serve(listener, NewMemoryStore()))
}
