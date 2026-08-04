package wslog

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gofiber/contrib/v3/socketio"
)

// Notification levels, mirrored by the web app's toast store.
const (
	LevelSuccess = "success"
	LevelError   = "error"
	LevelWarning = "warning"
	LevelInfo    = "info"
)

// Notification is the payload of the `notification` socket event. Where the
// `message` event relays the engine's raw process stream for the log drawer,
// `notification` carries API-originated outcomes (scheduler launches, action
// results) that the web app surfaces as toasts.
type Notification struct {
	Level   string `json:"level"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message"`
	Ts      int64  `json:"ts"`
}

// conns tracks live sockets by UUID. The socketio package keeps its own pool
// but only exposes broadcast of `message` events; named events need the
// per-connection EmitEvent, so we mirror the pool here (registered in
// wshandler, dropped on disconnect/close in loadHandlers).
var conns = struct {
	sync.RWMutex
	m map[string]*socketio.Websocket
}{m: map[string]*socketio.Websocket{}}

func trackConn(kws *socketio.Websocket) {
	conns.Lock()
	conns.m[kws.GetUUID()] = kws
	conns.Unlock()
}

func dropConn(kws *socketio.Websocket) {
	if kws == nil {
		return
	}
	conns.Lock()
	delete(conns.m, kws.GetUUID())
	conns.Unlock()
}

// Notify broadcasts a `notification` event to every connected client. Safe to
// call from any goroutine; with no clients connected it is a no-op. level
// should be one of the Level* constants.
func Notify(level, title, message string) {
	n := Notification{Level: level, Title: title, Message: message, Ts: time.Now().UnixMilli()}
	data, err := json.Marshal(n)
	if err != nil {
		fmt.Printf("wslog: marshal notification: %v\n", err)
		return
	}
	broadcast("notification", data)
}

// Emit broadcasts an arbitrary named event with a JSON payload to every
// connected client. It is the general form of Notify (which is `notification`
// with a toast shape): services push their own live updates on their own event
// name — the HITL chat service emits `hitl.message` with the updated task after
// each conversation turn, so an open conversation panel refreshes without
// polling. Safe from any goroutine; a marshal failure is logged and dropped.
func Emit(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("wslog: marshal %s event: %v\n", event, err)
		return
	}
	broadcast(event, data)
}

// broadcast enqueues a pre-marshalled event onto every live connection.
func broadcast(event string, data []byte) {
	conns.RLock()
	targets := make([]*socketio.Websocket, 0, len(conns.m))
	for _, kws := range conns.m {
		targets = append(targets, kws)
	}
	conns.RUnlock()
	for _, kws := range targets {
		// EmitEvent enqueues onto the per-connection send queue; a dead socket
		// fires EventError and is dropped by the disconnect handler.
		kws.EmitEvent(event, data)
	}
}
