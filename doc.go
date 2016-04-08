// Package juggler implements a websocket-based, redis-backed RPC and
// pub-sub server. RPC (remote procedure call) requests are routed
// to a URI, and the response is asynchronous and is only sent to
// the calling client. Pub-sub events are also asynchronous but
// are routed to every client subscribed to the channel on which the
// event is published. Only active clients receive the event - this is
// not to be confused with a reliable message queue.
//
// All messages sent by the client receive an acknowledge message
// (ACK) when processed successfully or a negative acknowledge (NACK)
// if the request was rejected. See the message package documentation
// for all details regarding the supported messages.
//
// Server
//
// The Server struct defines a juggler server. In its simplest form, the
// following initializes a ready-to-use server:
//
//     broker := &redisbroker.Broker{...} // initialize a broker
//     server := &juggler.Server{
//       PubSubBroker: broker,
//       CallerBroker: broker,
//     }
//
// That is, only the pub-sub and caller brokers must be set for the server
// to start serving connections. The broker is typically a redisbroker.Broker,
// although it can be any value that implements the broker.PubSubBroker and
// broker.CallerBroker interfaces, respectively.
//
// Additional fields allow for more advanced configuration, such as
// read and write timeouts and limits, and custom message handling,
// via the Handler. Metrics can be collected by setting the Vars field
// to an *expvar.Map. See the Server type documentation for all details.
//
// The ServeConn method serves a connection using a configured Server.
// The Upgrade function creates an http.Handler that upgrades the
// HTTP connection to a websocket connection, and serves it using the
// provided Server.
//
// Because HTTP/2 does not support websockets, the HTTP server used
// to run the juggler server must not use HTTP/2. Since Go1.6, HTTP/2
// is automatically enabled over HTTPS. See https://golang.org/doc/go1.6#http2
// for details on how to explicitly disable it.
//
// One or many callees must be registered to listen for RPC requests.
// The callees are decoupled from the server, with redis acting as the
// broker. The pub-sub part is handled natively by redis. See the
// callee package for details.
//
// Conn
//
// The Conn struct represents a websocket connection to a juggler
// server. To be accepted by the server, the connection must accept
// one of the subprotocols supported by the server (the Subprotocols
// package variable). The negociated subprotocol is available via
// the Subprotocol connection method.
//
// A connection listens for its RPC call results, pub-sub events and
// requests from the client end, and ensures the messages flow from client to
// server and back as needed.
//
// Some client connections may know ahead of time that they won't make
// any RPC calls, or won't subscribe to any pub-sub channel, etc. In that
// case, the Juggler-Allowed-Messages header can be set on the HTTP
// request that initiates the connection with a restricted list of allowed
// messages, e.g.:
//
//     http.Header{"Juggler-Allowed-Messages": {"call, pub"}}
//
// The value is a comma-separated list of allowed messages. When that header
// is non-empty and not *, only the specified messages are allowed. This
// leads to a more efficient server-side connection and ensures the
// connection behaves as advertised, otherwise the connection is closed.
//
// Handler
//
// By default, when Server.Handler is nil, each message sent or received
// by the Server goes through the ProcessMsg function, which implements
// the standard behaviour for each message type.
//
// A custom handler can be set to implement middleware-style behaviour,
// similar to the stdlib's net/http server. When Handler is not nil, it is
// the responsibility of the handler to eventually call ProcessMsg so
// that the messages produce the expected results.
//
// Typical use of handlers can be:
//
//     - to implement logging of requests/responses
//     - to recover in case of panics
//     - to implement authentication for some requests
//     - to implement authorization checks for some requests
//     - etc.
//
// If a handler detects that the message cannot be executed as requested,
// e.g. because the caller is not authenticated or doesn't have access
// to the requested RPC URI, ProcessMsg must not be called. The message
// must be "intercepted" before the call to ProcessMsg, and a NACK reply
// must be returned to the client to let it know that the request will
// not be processed.
//
// To do so, the Conn offers the Send method, for example:
//
//     func CheckAccessHandler(ctx context.Context, c *juggler.Conn, m message.Msg) {
//       // assume we detected that the caller doesn't have access, in the ok variable
//       if !ok {
//         nack := message.NewNack(m, 403, errors.New("caller doesn't have access"))
//         c.Send(nack)
//         return
//       }
//     }
//
// The Send method makes sure the provided message goes through the handler
// too, so typically a handler would implement conditional checks depending
// on the type of the message. Authentication and authorization, for example,
// only make sense on request messages (messages coming from the client),
// which have their Type.IsRead method return true. Responses (messages
// sent by the server) have their Type.IsWrite method return true.
//
// A new context.Context is passed for each message processed to maintain
// values for the duration of a specific message.
//
package juggler
