// Package wslog exposes the engine's process event stream over a WebSocket.
//
// The inflow layer subscribes to the NATS event log and, for every event,
// rebroadcasts the raw JSON to all connected sockets (see inflow.broadcastEvent).
// This package owns the socket endpoint and the connection lifecycle; it does not
// itself touch NATS, so there is no coupling between the HTTP layer and the
// engine wiring beyond the global socketio broadcast.
//
// Mirrors inspector-api's flow WebSocket, minus the JWT gate — the FloMorphic API
// runs unauthenticated by default (see api.RegisterAll), and the web app never
// mints a token in that mode. When auth is turned on the CRUD groups are guarded
// upstream; the socket stays open so the log drawer keeps working locally.
package wslog

import (
	"encoding/json"
	"fmt"
	"sync"

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
			fmt.Printf("wslog: client disconnected (%s)\n", ep.Kws.GetStringAttribute("sessId"))
		})
	})
}

// Register mounts the log socket. The `:id` segment is a caller-chosen session
// label (the web app uses a fixed one); it is not authenticated and only serves
// to give each connection a name in the logs.
func Register(app fiber.Router) {
	loadHandlers()
	app.Get("/ws/:id", socketio.New(wshandler))
}

func wshandler(kws *socketio.Websocket) {
	sessID := kws.Params("id")
	kws.SetAttribute("sessId", sessID)

	welcome, _ := json.Marshal(fmt.Sprintf("connected: %s", sessID))
	kws.Emit(welcome, socketio.TextMessage)
}
