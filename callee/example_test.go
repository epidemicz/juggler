package callee_test

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/PuerkitoBio/juggler/broker/redisbroker"
	"github.com/PuerkitoBio/juggler/callee"
	"github.com/PuerkitoBio/juggler/message"
	"github.com/garyburd/redigo/redis"
)

const (
	redisAddr = ":6060"
	nWorkers  = 10
	calleeURI = "example.echo"
)

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

// the Thunk for the example.echo URI, it simply extracts the arguments
// and acts as the wrapper/type-checker for the actual echo function.
func echoThunk(cp *message.CallPayload) (interface{}, error) {
	var s string
	if err := json.Unmarshal(cp.Args, &s); err != nil {
		return nil, err
	}
	return echo(s), nil
}

// simply return the same value that was received.
func echo(s string) string {
	return s
}
