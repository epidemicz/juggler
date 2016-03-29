// Command juggler-callee implements a testing callee that provides
// simple URI functions.
//
//     - test.echo (string) : returns the received string
//     - test.reverse (string) : reverses each rune in the received string
//     - test.delay (string) : sleeps for the duration received as string, converted to number (in ms)
//
package main

import (
	"encoding/json"
	"expvar"
	"flag"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/PuerkitoBio/juggler/broker"
	"github.com/PuerkitoBio/juggler/broker/redisbroker"
	"github.com/PuerkitoBio/juggler/callee"
	"github.com/PuerkitoBio/juggler/message"
	"github.com/PuerkitoBio/redisc"
	"github.com/garyburd/redigo/redis"
)

var (
	brokerBlockingTimeoutFlag = flag.Duration("broker-blocking-timeout", 0, "Blocking `timeout` when polling for call requests.")
	brokerResultCapFlag       = flag.Int("broker-result-cap", 0, "Capacity of the `results` queue.")
	helpFlag                  = flag.Bool("help", false, "Show help.")
	numDelayURIsFlag          = flag.Int("n", 0, "Number of test.delay `URIs`.")
	httpServerPortFlag        = flag.Int("port", 9001, "HTTP server `port` to serve debug endpoints.")
	redisAddrFlag             = flag.String("redis", ":6379", "Redis `address`.")
	redisClusterFlag          = flag.Bool("redis-cluster", false, "Use redis cluster.")
	redisPoolIdleTimeoutFlag  = flag.Duration("redis-idle-timeout", 0, "Redis idle connection `timeout`.")
	redisPoolMaxActiveFlag    = flag.Int("redis-max-active", 0, "Maximum active redis `connections`.")
	redisPoolMaxIdleFlag      = flag.Int("redis-max-idle", 0, "Maximum idle redis `connections`.")
	workersFlag               = flag.Int("workers", 1, "Number of concurrent `workers` processing call requests.")
)

var uris = map[string]callee.Thunk{
	"test.echo":    echoThunk,
	"test.reverse": reverseThunk,
	"test.delay":   delayThunk,
}

func main() {
	flag.Parse()
	if *helpFlag {
		flag.Usage()
		return
	}
	if *workersFlag <= 0 {
		*workersFlag = 1
	}

	for i := 0; i < *numDelayURIsFlag; i++ {
		uris["test.delay."+strconv.Itoa(i)] = delayThunk
	}

	var pool redisbroker.Pool
	var dial func() (redis.Conn, error)

	if *redisClusterFlag {
		cluster := newRedisCluster(*redisAddrFlag)
		pool, dial = cluster, cluster.Dial
	} else {
		p, _ := newRedisPool(*redisAddrFlag)
		pool, dial = p, p.Dial
	}
	c := &callee.Callee{Broker: newBroker(pool, dial)}

	vars := expvar.NewMap("callee")

	// start a web server to serve pprof and expvar data
	log.Printf("serving debug endpoints on %d", *httpServerPortFlag)
	go func() {
		log.Println(http.ListenAndServe(":"+strconv.Itoa(*httpServerPortFlag), nil))
	}()

	log.Printf("listening for call requests on %s with %d workers", *redisAddrFlag, *workersFlag)
	keys := make([]string, 0, len(uris))
	for k := range uris {
		keys = append(keys, k)
	}

	// TODO : split by slot in cluster mode...
	cc, err := c.Broker.NewCallsConn(keys...)
	if err != nil {
		log.Fatalf("Calls failed: %v", err)
	}
	defer cc.Close()

	wg := sync.WaitGroup{}
	wg.Add(*workersFlag)
	for i := 0; i < *workersFlag; i++ {
		go func() {
			defer wg.Done()

			ch := cc.Calls()
			for cp := range ch {
				log.Printf("received request %v %s", cp.MsgUUID, cp.URI)
				vars.Add("Requests", 1)
				vars.Add("Requests."+cp.URI, 1)

				if err := c.InvokeAndStoreResult(cp, uris[cp.URI]); err != nil {
					if err != callee.ErrCallExpired {
						log.Printf("InvokeAndStoreResult failed: %v", err)
						vars.Add("Failed", 1)
						vars.Add("Failed."+cp.URI, 1)
						continue
					}
					log.Printf("expired request %v %s", cp.MsgUUID, cp.URI)
					vars.Add("Expired", 1)
					vars.Add("Expired."+cp.URI, 1)
					continue
				}
				log.Printf("sent result %v %s", cp.MsgUUID, cp.URI)
				vars.Add("Succeeded", 1)
				vars.Add("Succeded."+cp.URI, 1)
			}
		}()
	}
	wg.Wait()
}

func logWrapThunk(t callee.Thunk) callee.Thunk {
	return func(cp *message.CallPayload) (interface{}, error) {
		log.Printf("received call for %s from %v", cp.URI, cp.MsgUUID)
		v, err := t(cp)
		log.Printf("sending result for %s from %v", cp.URI, cp.MsgUUID)
		return v, err
	}
}

func delayThunk(cp *message.CallPayload) (interface{}, error) {
	var s string
	if err := json.Unmarshal(cp.Args, &s); err != nil {
		return nil, err
	}
	i, err := strconv.Atoi(s)
	if err != nil {
		return nil, err
	}
	return delay(i), nil
}

func delay(i int) int {
	time.Sleep(time.Duration(i) * time.Millisecond)
	return i
}

func reverseThunk(cp *message.CallPayload) (interface{}, error) {
	var s string
	if err := json.Unmarshal(cp.Args, &s); err != nil {
		return nil, err
	}
	return reverse(s), nil
}

func reverse(s string) string {
	chars := []rune(s)
	for i, j := 0, len(chars)-1; i < j; i, j = i+1, j-1 {
		chars[i], chars[j] = chars[j], chars[i]
	}
	return string(chars)
}

func echoThunk(cp *message.CallPayload) (interface{}, error) {
	var s string
	if err := json.Unmarshal(cp.Args, &s); err != nil {
		return nil, err
	}
	return echo(s), nil
}

func echo(s string) string {
	return s
}

func newBroker(pool redisbroker.Pool, dial func() (redis.Conn, error)) broker.CalleeBroker {
	return &redisbroker.Broker{
		Pool:            pool,
		Dial:            dial,
		BlockingTimeout: *brokerBlockingTimeoutFlag,
		ResultCap:       *brokerResultCapFlag,
	}
}

func newRedisCluster(addr string) *redisc.Cluster {
	return &redisc.Cluster{
		StartupNodes: []string{addr},
		CreatePool:   newRedisPool,
	}
}

func newRedisPool(addr string, opts ...redis.DialOption) (*redis.Pool, error) {
	return &redis.Pool{
		MaxIdle:     *redisPoolMaxIdleFlag,
		MaxActive:   *redisPoolMaxActiveFlag,
		IdleTimeout: *redisPoolIdleTimeoutFlag,
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
	}, nil
}
