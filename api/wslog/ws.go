// Package wslog exposes the engine's process event stream over a WebSocket.
//
// The inflow layer subscribes to the NATS event log and, for every event,
// rebroadcasts the raw JSON to all connected sockets (see inflow.broadcastEvent).
// This package owns the socket endpoint and the connection lifecycle; it does not
// itself touch NATS, so there is no coupling between the HTTP layer and the
// engine wiring beyond the global socketio broadcast.
//
// Auth: when AuthEnabled the socket is gated like the CRUD groups, but with the
// token read from the handshake query string as well as the header — a browser
// cannot set headers on a WebSocket upgrade. Without auth the socket is open,
// which is the default and how the app runs standalone.
package wslog

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/gofiber/contrib/v3/socketio"
	"github.com/gofiber/fiber/v3"
)

// loadHandlersOnce guards the socketio event-handler registration. socketio.On
// appends a callback per call, so registering twice would fire every lifecycle
// event twice.
var loadHandlersOnce sync.Once

func loadHandlers() {
	loadHandlersOnce.Do(func() {
		socketio.On(socketio.EventConnect, func(ep *socketio.EventPayload) {
			fmt.Printf("wslog: client connected (%s)\n", ep.Kws.GetStringAttribute("sessId"))
		})
		socketio.On(socketio.EventDisconnect, func(ep *socketio.EventPayload) {
			dropConn(ep.Kws)
			fmt.Printf("wslog: client disconnected (%s)\n", ep.Kws.GetStringAttribute("sessId"))
		})
		// Server-side close does not fire EventDisconnect; drop the conn here too.
		socketio.On(socketio.EventClose, func(ep *socketio.EventPayload) {
			dropConn(ep.Kws)
		})
	})
}

// Register mounts the log socket. The `:id` segment is a caller-chosen session
// label (the web app uses a fixed one); it only serves to give each connection a
// name in the logs.
//
// When AuthEnabled is set the route is gated by HS256SocketKeyHandler, which
// accepts the bearer in the handshake query string as well as the header.
// Without it the socket was the one unguarded surface on an otherwise guarded
// install.
func Register(app fiber.Router) {
	loadHandlers()
	if env.AuthEnabled() {
		app.Get("/ws/:id", etc.HS256SocketKeyHandler(), socketio.New(wshandler))
		return
	}
	app.Get("/ws/:id", socketio.New(wshandler))
}

func wshandler(kws *socketio.Websocket) {
	sessID := kws.Params("id")
	kws.SetAttribute("sessId", sessID)
	trackConn(kws) // enables named-event broadcasts (see notify.go)

	welcome, _ := json.Marshal(fmt.Sprintf("connected: %s", sessID))
	kws.Emit(welcome, socketio.TextMessage)
}
