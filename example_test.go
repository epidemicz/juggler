package juggler_test

import (
	"log"
	"net/http"
	"time"

	"github.com/PuerkitoBio/juggler"
	"github.com/PuerkitoBio/juggler/broker/redisbroker"
	"github.com/garyburd/redigo/redis"
	"github.com/gorilla/websocket"
)

// This example shows how to set up a juggler server and serve connections.
func Example() {
	const redisAddr = ":6379"

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
		Pool:    pool,
		Dial:    pool.Dial,
		CallCap: 1000,
	}

	// create a juggler server using the broker (in this case, use the
	// same redis server for RPC and pub-sub).
	server := &juggler.Server{
		CallerBroker: broker,
		PubSubBroker: broker,
	}

	// create a websocket connection upgrader, that will negociate the
	// subprotocol using the list of supported juggler subprotocols.
	upgrader := &websocket.Upgrader{
		Subprotocols: juggler.Subprotocols,
	}
	// get the upgrade HTTP handler and register it as handler for the
	// /ws path.
	upgh := juggler.Upgrade(upgrader, server)
	http.Handle("/ws", upgh)

	// start the HTTP server, connect to :9000/ws to make juggler connections.
	if err := http.ListenAndServe(":9000", nil); err != nil {
		log.Fatalf("ListenAndServe failed: %v", err)
	}
}
