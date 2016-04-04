// Package juggler implements a websocket-based, redis-backed RPC and
// pub-sub server.
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
// broker.CallerBroker interfaces.
//
// Additional fields allow for more advanced configuration, such as
// read and write timeouts, and custom message handling, via the Handler.
// Metrics can be collected by setting the Vars field to an *expvar.Map.
// See the Server type documentation for all details.
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
//
//
package juggler
