## juggler - websocket-based, redis-backed RPC and pub-sub [![GoDoc](https://godoc.org/github.com/PuerkitoBio/juggler?status.png)](http://godoc.org/github.com/PuerkitoBio/juggler) [![build status](https://secure.travis-ci.org/PuerkitoBio/juggler.png)](http://travis-ci.org/PuerkitoBio/juggler)

Juggler implements highly decoupled, asynchronous RPC and pub-sub over websocket connections using redis as broker. It refers both to a websocket subprotocol and the implementation of a juggler server. The repository also contains implementations of the callee, broker and client roles.

**This is still experimental. Use at your own risk. Not battle-tested in production environment. API may change. Javascript (and other languages) client not implemented yet.**

* [Architecture](#architecture)
* [Goals](#goals)
* [Installation](#installation)
* [Documentation](#documentation)
* [Getting Started](#getting-started)
* [Performance](#performance)
* [License](#license)

### Architecture

In a nutshell, the architecture looks like this:

```
     +------------+
     |            |-+
     | (a) Client | |
     |            | |
     +------------+ |
       +------------+
           ^
           |
           v
     +------------+
     |            |
     | (b) Server |
     |            |
     +------------+
           ^
           |
           v
     +-----------+
     |           |
     | (c) Redis |
     |           |
     +-----------+
           ^
           |
           v
     +------------+
     |            |-+
     | (d) Callee | |
     |            | |
     +------------+ |
       +------------+
```

* (a) Client is a juggler client.
    - It can be any kind of client - a web browser, a mobile application, a message queue worker process, anything that can make websocket connections.
    - It only communicates with the juggler server.
    - It can make RPC requests (CALL), subscribe to (SUB) and unsubscribe from (UNSB) pub-sub channels, and publish events (PUB).

* (b) Server is the juggler server.
    - It listens for websocket connections and accepts clients that support the juggler subprotocol.
    - It acknowledges (ACK or NACK in case of failure) RPC and pub-sub client requests, and sends RPC results (RES) and pub-sub events (EVNT) to the clients.
    - It uses a `broker.CallerBroker` to make RPC calls and a `broker.PubSubBroker` to handle pub-sub subscriptions and events via redis.
    - For scalability and high availability, multiple servers can be used behind a websocket load balancer, e.g. using [Caddy][].

* (c) Redis is the broker.
    - For RPC requests, the call payload is stored in a list with a corresponding key holding its time-to-live (TTL) before the request expires. Callees listen for calls on those lists, execute the corresponding function, and store the result payload in another list identified by the client connection.
    - For pub-sub, the native pub-sub support of redis is used.
    - For scalability and high availability, redis cluster is supported, and a different redis server (or cluster) can be used for RPC and for pub-sub.

* (d) Callee exposes RPC functions via a URI.
    - It listens for call requests, executes the corresponding function, and stores the result payload via a `broker.CalleeBroker` interface, which is responsible for the communication with redis.
    - It can be added and removed completely independently of the running application.
    - It only depends on redis, not on any other part of the system.

### Goals

The project was initially inspired by [the Web Application Messaging Protocol (WAMP)][wamp]. This is an application protocol that aims to work on a variety of transports (including websockets) with a focus on supporting embedded devices / the "internet of things", and any peer can be a caller and a callee. The specification is still a work-in-progress and as such, the protocol is a bit of a moving target, but it is a very interesting and ambitious project that you should check out if you're interested in something like this.

While trying to implement a WAMP router, I started thinking about a simpler, less ambitious implementation that would support what I feel are the most typically useful features using a battle-tested, scalable backend as broker - redis - instead of having a new piece of software manage the state (the `router` in the WAMP specification). Out-of-the-box, redis supports horizontal scaling and failover via the redis cluster, and it natively supports pub-sub. It also offers all the required primitives to handle RPC.

The goals of the juggler protocol and implementation are, in no specific order:

* Simplicity - the "protocol" is really just a pre-defined set of JSON-encoded messages exchanged over websockets: "CALL", "SUB", "UNSB" and "PUB" for clients, "ACK, "NACK", "RES" and "EVNT" for servers.
* Minimalism - it offers basic RPC and pub-sub primitives, leaving more specific behaviour to the applications.
* Scalability - via redis cluster and a websocket load balancer in front of multiple juggler servers, and independently managed instances of callees, there is scale-out support for juggler-based applications.
* Focused on web application development - web browsers and mobile applications are the target clients, embedded devices are not an explicit concern.

### Installation

Make sure you have the [Go programming language properly installed][go], then run in a terminal:

```
$ go get [-u] [-t] github.com/PuerkitoBio/juggler/...
```

The juggler packages use the following external dependencies (excluding test dependencies):

* [github.com/PuerkitoBio/redisc][redisc]
* [github.com/garyburd/redigo][redigo]
* [github.com/gorilla/websocket][websocket]
* [github.com/pborman/uuid][uuid]
* [golang.org/x/net/context][context]

### Documentation

The [godoc package documentation][godoc] is the canonical source of documentation. This README provides additional documentation of high-level usage of the various packages.

### Getting Started

#### Implement an RPC callee

```
// from package callee, file example_test.go
func Example() {
	// create a redis pool
	pool := &redis.Pool{
		MaxIdle:     10,
		MaxActive:   100,
		IdleTimeout: 30 * time.Second,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", redisAddr)
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}

	// create a redis broker
	broker := &redisbroker.Broker{
		Pool:      pool,
		Dial:      pool.Dial,
		ResultCap: 1000,
	}

	// create the callee
	c := &callee.Callee{Broker: broker}

	// get the Calls connection, that will stream the call requests to
	// the specified URIs (here, only one) on a channel.
	cc, err := broker.NewCallsConn(calleeURI)
	if err != nil {
		log.Fatalf("NewCallsConn failed: %v", err)
	}
	defer cc.Close()

	// start n workers
	wg := sync.WaitGroup{}
	wg.Add(nWorkers)
	for i := 0; i < nWorkers; i++ {
		go func() {
			defer wg.Done()

			// cc.Calls returns the same channel on each call, so all
			// workers actually process requests from the same channel.
			ch := cc.Calls()
			for cp := range ch {
				if err := c.InvokeAndStoreResult(cp, echoThunk); err != nil {
					if err != callee.ErrCallExpired {
						log.Printf("InvokeAndStoreResult failed: %v", err)
						continue
					}
					log.Printf("expired request %v %s", cp.MsgUUID, cp.URI)
					continue
				}
				log.Printf("sent result %v %s", cp.MsgUUID, cp.URI)
			}
		}()
	}
	wg.Wait()

	// runs forever, in a main command we could listen to a signal
	// using signal.Notify and close the Calls connection when e.g. SIGINT
	// is received, and then wait on the wait group for proper termination.
}
```

#### Implement a juggler server

#### Experiment in docker

The repository contains docker and docker-compose files to quickly start a test juggler environment. It starts a redis node, a juggler server, a callee and a client. Provided you have [docker properly installed][docker], run the following in the juggler repository's root directory:

```
# for convenience, use the COMPOSE_FILE environment variable to avoid typing
# the compose file every time.
$ export COMPOSE_FILE=docker/docker-compose.1.yml

$ docker-compose build

$ docker-compose up -d

$ docker-compose run client

# once done playing with the test environment, run:
$ docker-compose stop
```

This will start the interactive client to make calls to the juggler server. This test environment registers 3 RPC URIs:

* `test.echo` : returns whatever string was passed as parameter.
* `test.reverse` : reverses the string passed as parameter.
* `test.delay` : sleeps for N millisecond, N being the number passed as parameter.

Enter `connect` to start a new connection (you can start many connections in the same interactive session). Enter `help` to get the list of available commands and expected arguments. Type `exit` to terminate the session.

### Performance

Juggler has been load-tested in various configurations on digital ocean's droplets. Before talking about "how fast" it is, it is useful to first define "fast" in relative terms. Tests have been executed on a DO droplet for redis LPUSH and BRPOP to figure out the maximum throughput, then with tests closer to what juggler does (juggler doesn't just push a value on a list, it marshals/unmarshals a struct to JSON, and runs a lua script in redis to push the value on a list and set an expiration key atomically, so each RPC call has a TTL after which the result is discarded). This helps get a good idea of what the ideal throughput can be, and then compare that to the actual juggler throughput.

All tests were run on the smallest 512Mb droplet in the Toronto region. The redis tests are executed locally. The juggler tests go through the whole layers (client <-> server <-> redis <-> callee) using the following nodes:

* 1 droplet for redis (3 for redis cluster)
* 1 droplet for the callee
* 1 droplet for the server (3 for the load balancing test)
* 1 droplet for the Caddy load balancer (only for the load balancing test)
* 1 droplet for the load tester

<table>
    <thead>
        <tr>
            <th>Scenario</th>
            <th>Throughput</th>
        </tr>
    </thead>
    <tbody>
        <tr>
            <td>Pure LPUSH / BRPOP (local test)</td>
            <td>13000 / second</td>
        </tr>
        <tr>
            <td>LPUSH / BRPOP with JSON marshal/unmarshal of struct (local test)</td>
            <td>10000 / second</td>
        </tr>
        <tr>
            <td>JSON structs with Lua script that handles the list and the TTL key (local test)</td>
            <td>4200 / second</td>
        </tr>
        <tr>
            <td>Juggler clients RPC calls (single server, standalone redis)</td>
            <td>2000 / second</td>
        </tr>
        <tr>
            <td>Juggler clients RPC calls (3 load-balanced servers, 3 nodes redis cluster)</td>
            <td>3000 / second</td>
        </tr>
    </tbody>
</table>

The numbers per second for the juggler tests represent a full CALL - ACK - RES, meaning the call was processed by the calle and the result was received by the client. The client-measured latencies during those juggler load tests was below 100ms per call for the median, and below 400ms for the 99th percentile. Pub-sub was not stress-tested as it uses the native redis feature and there are some benchmarks already available for this. As always with benchmarks, you should run your own in your own environment and representative use-case. There is a script in `misc/perf/run-do-test.sh` that can help you with this, make sure you understand the script before running it using your digital ocean account. Many raw test results are available in this `misc/perf` directory.

### License

The [BSD 3-Clause license][bsd], the same as the Go language.

[caddy]: https://caddyserver.com/
[godoc]: https://godoc.org/github.com/PuerkitoBio/juggler
[bsd]: http://opensource.org/licenses/BSD-3-Clause
[go]: https://golang.org/doc/install
[docker]: https://docs.docker.com/machine/install-machine/
[redisc]: https://github.com/PuerkitoBio/redisc
[redigo]: https://github.com/garyburd/redigo
[websocket]: https://github.com/gorilla/websocket
[uuid]: https://github.com/pborman/uuid
[context]: https://godoc.org/golang.org/x/net/context
[wamp]: http://wamp-proto.org/

