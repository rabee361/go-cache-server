package main

import (
	"fmt"
	"strings"
	"time"
)

type commandHandler func(store Store, args []string) Reply

type commandSpec struct {
	name    string
	minArgs int
	maxArgs int // -1 = unbounded
	handler commandHandler
}

var commandTable = map[string]commandSpec{
	"PING":   {"PING", 1, 2, cmdPing},
	"SET":    {"SET", 3, 3, cmdSet},
	"GET":    {"GET", 2, 2, cmdGet},
	"DEL":    {"DEL", 2, -1, cmdDel},
	"EXISTS": {"EXISTS", 2, -1, cmdExists},
	"READY":  {"READY", 1, 1, cmdReady},
	"QUIT":   {"QUIT", 1, 1, cmdQuit},
}

// dispatch routes a decoded command to its handler. It normalizes the command
// name to upper case and checks arity against the command table, so handlers
// can index args without bounds checks.
func dispatch(store Store, args []string) Reply {
	if len(args) == 0 {
		return errReply("ERR empty command")
	}
	name := strings.ToUpper(args[0])
	spec, ok := commandTable[name]
	if !ok {
		return errReply(fmt.Sprintf("ERR unknown command '%s'", args[0]))
	}
	if len(args) < spec.minArgs || (spec.maxArgs >= 0 && len(args) > spec.maxArgs) {
		return errReply(fmt.Sprintf("ERR wrong number of arguments for '%s' command", name))
	}
	return spec.handler(store, args)
}

func cmdPing(store Store, args []string) Reply {
	if len(args) == 2 {
		return bulkReply([]byte(args[1]))
	}
	return simpleString("PONG")
}

func cmdSet(store Store, args []string) Reply {
	if _, err := store.Set(args[1], []byte(args[2])); err != nil {
		return errReply("ERR set failed: " + err.Error())
	}
	return simpleString("OK")
}

func cmdGet(store Store, args []string) Reply {
	v, ok := store.Get(args[1])
	if !ok {
		return nullReply()
	}
	return bulkReply(v)
}

func cmdDel(store Store, args []string) Reply {
	var n int64
	for _, k := range args[1:] {
		c, err := store.Delete(k)
		if err != nil {
			return errReply("ERR delete failed: " + err.Error())
		}
		n += int64(c)
	}
	return intReply(n)
}

func cmdExists(store Store, args []string) Reply {
	var n int64
	for _, k := range args[1:] {
		ok, err := store.Exists(k)
		if err != nil {
			return errReply("ERR exists failed: " + err.Error())
		}
		if ok {
			n++
		}
	}
	return intReply(n)
}

func cmdReady(store Store, args []string) Reply {
	key := fmt.Sprintf("_health_check_%d", time.Now().UnixNano())
	testValue := []byte("ok")

	cleanup := func() { store.Delete(key) }

	if _, err := store.Set(key, testValue); err != nil {
		cleanup()
		return errReply("ERR readiness check failed: " + err.Error())
	}

	got, ok := store.Get(key)
	if !ok {
		cleanup()
		return errReply("ERR readiness check failed: key not found after set")
	}

	if string(got) != string(testValue) {
		cleanup()
		return errReply("ERR readiness check failed: get returned wrong value")
	}

	if _, err := store.Delete(key); err != nil {
		return errReply("ERR readiness check failed: delete: " + err.Error())
	}

	return simpleString("READY")
}

func cmdQuit(store Store, args []string) Reply {
	return simpleString("OK")
}
