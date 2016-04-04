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
// Conn
//
//
//
// TODO : more and better doc here, + readme + license
package juggler
