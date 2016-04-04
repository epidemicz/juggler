package juggler

import (
	"expvar"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/juggler/broker"
	"github.com/PuerkitoBio/juggler/message"
	"github.com/gorilla/websocket"
)

// Subprotocols is the list of juggler protocol versions supported by this
// package. It should be set as-is on the websocket.Upgrader Subprotocols
// field.
var Subprotocols = []string{
	"juggler.0",
}

func isInStr(list []string, v string) bool {
	for _, vv := range list {
		if vv == v {
			return true
		}
	}
	return false
}

// Server is a juggler server. Once a websocket handshake has been
// established with a juggler subprotocol over a standard HTTP server,
// the connections can get served by this server by calling
// Server.ServeConn.
//
// The fields should not be updated once a server has started
// serving connections.
type Server struct {
	// ReadLimit defines the maximum size, in bytes, of incoming
	// messages. If a client sends a message that exceeds this limit,
	// the connection is closed. The default of 0 means no limit.
	ReadLimit int64

	// ReadTimeout is the timeout to read an incoming message. It is
	// set on the websocket connection with SetReadDeadline before
	// reading each message. The default of 0 means no timeout.
	ReadTimeout time.Duration

	// WriteLimit defines the maximum size, in bytes, of outgoing
	// messages. If a message exceeds this limit, the connection is
	// closed. The default of 0 means no limit.
	WriteLimit int64

	// WriteTimeout is the timeout to write an outgoing message. It is
	// set on the websocket connection with SetWriteDeadline before
	// writing each message. The default of 0 means no timeout.
	WriteTimeout time.Duration

	// AcquireWriteLockTimeout is the time to wait for the exclusive
	// write lock for a connection. If the lock cannot be acquired
	// before the timeout, the connection is dropped. The default of
	// 0 means no timeout.
	AcquireWriteLockTimeout time.Duration

	// ConnState specifies an optional callback function that is called
	// when a connection changes state. If non-nil, it is called for
	// Accepting, Connected and Closing states. Closing means closing the
	// juggler connection, the underlying websocket connection may stay
	// connected.
	//
	// The possible state transitions are:
	//
	//     Accepting -> Closing (if the server failed to setup the connection)
	//     Accepting -> Connected
	//     Connected -> Closing
	ConnState func(*Conn, ConnState)

	// Handler is the handler that is called when a message is
	// processed. The ProcessMsg function is called if the default
	// nil value is set. If a custom handler is set, it is assumed
	// that it will call ProcessMsg at some point, or otherwise
	// manually process the messages.
	Handler Handler

	// PubSubBroker is the broker to use for pub-sub messages. It must be
	// set before the Server can be used.
	PubSubBroker broker.PubSubBroker

	// CallerBroker is the broker to use for caller messages. It must be
	// set before the server can be used.
	CallerBroker broker.CallerBroker

	// Vars can be set to an *expvar.Map to collect metrics about the
	// server.
	Vars *expvar.Map
}

var allReqMsgs = []message.Type{message.CallMsg, message.SubMsg, message.UnsbMsg, message.PubMsg}

func isInType(list []message.Type, v message.Type) bool {
	for _, vv := range list {
		if vv == v {
			return true
		}
	}
	return false
}

// ServeConn serves the websocket connection as a juggler connection. It
// blocks until the juggler connection is closed, leaving the websocket
// connection open. If allowedMsgs is not empty, only those message types
// are allowed on that connection.
func (srv *Server) ServeConn(conn *websocket.Conn, allowedMsgs ...message.Type) {
	if srv.Vars != nil {
		srv.Vars.Add("ActiveConns", 1)
		srv.Vars.Add("TotalConns", 1)
		defer srv.Vars.Add("ActiveConns", -1)
	}

	conn.SetReadLimit(srv.ReadLimit)
	c := newConn(conn, srv, allowedMsgs...)
	if len(allowedMsgs) == 0 {
		allowedMsgs = allReqMsgs
	}

	// start lifecycle - Accepting, and ensure Closing is called on exit
	if cs := srv.ConnState; cs != nil {
		defer func() {
			cs(c, Closing)
		}()
		cs(c, Accepting)
	}

	// setup results connection if CALL is allowed
	callOK := isInType(allowedMsgs, message.CallMsg)
	if callOK {
		resConn, err := srv.CallerBroker.NewResultsConn(c.UUID)
		if err != nil {
			c.Close(fmt.Errorf("failed to create results connection: %v; dropping connection", err))
			return
		}
		c.resc = resConn
	}

	// set pub-sub connection that handles sub and unsb messages
	subOK, unsbOK := isInType(allowedMsgs, message.SubMsg),
		isInType(allowedMsgs, message.UnsbMsg)
	if subOK || unsbOK {
		pubSubConn, err := srv.PubSubBroker.NewPubSubConn()
		if err != nil {
			c.Close(fmt.Errorf("failed to create pubsub connection: %v; dropping connection", err))
			return
		}
		c.psc = pubSubConn
	}

	// switch to connected state
	if cs := srv.ConnState; cs != nil {
		cs(c, Connected)
	}

	// receive, results, pub/sub loops
	if subOK {
		// can't receive events unless SUB is allowed
		go c.pubSub()
	}
	if callOK {
		go c.results()
	}
	go c.receive()

	kill := c.CloseNotify()
	<-kill
}

// Upgrade returns an http.Handler that upgrades connections to
// the websocket protocol using upgrader. The websocket connection
// must be upgraded to a supported juggler subprotocol otherwise
// the connection is dropped.
//
// Once connected, the websocket connection is served via srv.ServeConn.
// The websocket connection is closed when the juggler connection is closed.
//
// If the Juggler-Allowed-Messages header is set on the request, the
// connection is restricted to that set of message types. The value
// is a comma-separated list of request message types:
//
//     Any of "call, sub, unsb, pub"
//     "*" can be used for any message type (same as if the header wasn't there)
//
func Upgrade(upgrader *websocket.Upgrader, srv *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// upgrade the HTTP connection to the websocket protocol
		wsConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer wsConn.Close()

		// the agreed-upon subprotocol must be one of the supported ones.
		if !isInStr(Subprotocols, wsConn.Subprotocol()) {
			return
		}

		var msgs []message.Type
		if allowed := strings.TrimSpace(r.Header.Get("Juggler-Allowed-Messages")); allowed != "" && allowed != "*" {
			types := strings.Split(allowed, ",")
			for _, typ := range types {
				typ := strings.TrimSpace(strings.ToLower(typ))
				switch typ {
				case "call":
					msgs = append(msgs, message.CallMsg)
				case "sub":
					msgs = append(msgs, message.SubMsg)
				case "unsb":
					msgs = append(msgs, message.UnsbMsg)
				case "pub":
					msgs = append(msgs, message.PubMsg)
				}
			}
		}

		// this call blocks until the juggler connection is closed
		srv.ServeConn(wsConn, msgs...)
	})
}
