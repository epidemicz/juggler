# juggler - websocket-based, redis-backed RPC and pub-sub [![GoDoc](https://godoc.org/github.com/PuerkitoBio/juggler?status.png)](http://godoc.org/github.com/PuerkitoBio/juggler) [![build status](https://secure.travis-ci.org/PuerkitoBio/juggler.png)](http://travis-ci.org/PuerkitoBio/juggler)

Juggler implements highly decoupled, asynchronous RPC and pub-sub over websocket connections using redis as broker. It refers both to a websocket subprotocol and the implementation of a juggler server. The repository also contains implementations of the callee, broker and client roles.

In a nutshell, the architecture looks like this:

```
+-----------+                 +------------+             +------------+
|           |                 |            |             |            |-+
| (a) Redis | <-------------> | (c) Server |<----------->| (d) Client | |
|           |                 |            |             |            | |
+-----------+                 +------------+             +------------+ |
      ^                                                    +------------+
      |                        
      v
+------------+
|            |--+
| (b) Callee |  |--+
|            |  |  |
+------------+  |  |
   +------------+  |
      +------------+
```

* (a) Redis is the broker.
    - For RPC requests, the call payload is stored in a list identified by a URI. Callees listen for calls on those lists, execute the associated function, and store the result payload in another list identified to the client connection.
    - For pub-sub, the native pub-sub support of redis is used.
    - For scalability and high availability, redis cluster is supported, and a different redis server (or cluster) can be used for RPC and for pub-sub.

* (b) Callee exposes RPC functions via a URI.
    - It listens for call requests, executes the corresponding function, and stores the result payload via the `broker.CalleeBroker` interface, which is responsible for the communication with redis.
    - It can be added and removed completely independently of the running application.
    - It only depends on redis, not on any other part of the system.

* (c) Server is the juggler server.
    - It listens for websocket connections and accepts clients that support the juggler subprotocol.
    - It acknowledges (ACK) or negative-acknowledges in case of failure (NACK) RPC and pub-sub client requests, and sends RPC results (RES) and pub-sub events (EVNT) to the clients.
    - It uses the `broker.CallerBroker` to make RPC calls and the `broker.PubSubBroker` to handle pub-sub subscriptions and events via redis.
    - For scalability and high availability, multiple servers can be used behind a websocket load balancer, e.g. using [caddy][].

* (d) Client is a juggler client.
    - It can be any kind of client - a web browser, a mobile application, a message queue worker process, anything that can make websocket connections.
    - It only communicates with the juggler server.
    - It can make RPC requests (CALL), subscribe to (SUB) and unsubscribe from (UNSB) pub-sub channels, and publish events (PUB).

## Goals

## Install

Make sure you have the [Go programming language properly installed][go], then run in a terminal:

```
$ go get [-u] [-t] github.com/PuerkitoBio/juggler/...
```

## Documentation

The [godoc package documentation][godoc] is the canonical source of documentation. This README provides additional documentation of high-level usage of the various packages.

## Getting Started

## Performance

## License

The [BSD 3-Clause license][bsd], the same as the Go language.

[caddy]: https://caddyserver.com/
[godoc]: https://godoc.org/github.com/PuerkitoBio/juggler
[bsd]: http://opensource.org/licenses/BSD-3-Clause
[go]: https://golang.org/doc/install

