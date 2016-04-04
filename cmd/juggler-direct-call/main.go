// Command juggler-direct-call implements a test caller that directly sends
// to redis, without a server and a client. It is used for testing the
// throughput of a callee without going through the whole layers.
package main

import (
	"flag"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/juggler/broker"
	"github.com/PuerkitoBio/juggler/broker/redisbroker"
	"github.com/PuerkitoBio/juggler/message"
	"github.com/garyburd/redigo/redis"
	"github.com/pborman/uuid"
)

var (
	durationFlag  = flag.Duration("d", 10*time.Second, "Duration of the test.")
	redisAddrFlag = flag.String("redis", ":6379", "Redis `address`.")
	timeoutFlag   = flag.Duration("t", time.Second, "`Timeout` of the call.")
)

func main() {
	flag.Parse()

	pool := newRedisPool(*redisAddrFlag)
	broker := newBroker(pool, pool.Dial)

	var calls, results int64
	connUUID := uuid.NewRandom()
	c, err := broker.NewResultsConn(connUUID)
	if err != nil {
		log.Fatalf("NewResultsConn failed: %v", err)
	}
	defer c.Close()
	for i := 0; i < 100; i++ {
		go func() {
			for range c.Results() {
				atomic.AddInt64(&results, 1)
			}
		}()
	}

	done := time.After(*durationFlag)
loop:
	for {
		select {
		case <-done:
			break loop
		default:
		}
		cp := &message.CallPayload{
			ConnUUID: connUUID,
			MsgUUID:  uuid.NewRandom(),
			URI:      "test.delay",
			Args:     []byte("0"),
		}
		if err := broker.Call(cp, *timeoutFlag); err != nil {
			log.Fatalf("Call failed: %v", err)
		}
		calls++
	}
	time.Sleep(*timeoutFlag)

	fmt.Printf("calls: %d, results: %d, timeout: %s\n", calls, atomic.LoadInt64(&results), *timeoutFlag)
}

func newBroker(pool redisbroker.Pool, dial func() (redis.Conn, error)) broker.CallerBroker {
	return &redisbroker.Broker{
		Pool: pool,
		Dial: dial,
	}
}

func newRedisPool(addr string) *redis.Pool {
	return &redis.Pool{
		MaxIdle: 100,
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("tcp", addr)
			if err != nil {
				return nil, err
			}
			return c, err
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}
}
