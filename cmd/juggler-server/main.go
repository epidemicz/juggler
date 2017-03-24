// Command juggler-server implements a juggler server that listens for
// connections and serves the requests. It is mostly useful as a testing
// and debugging tool, typical applications will use the juggler package
// as a library in their own main command.
package main

import (
	"expvar"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"golang.org/x/net/context"

	"github.com/garyburd/redigo/redis"
	"github.com/gorilla/websocket"
	"github.com/mna/juggler"
	"github.com/mna/juggler/broker"
	"github.com/mna/juggler/broker/redisbroker"
	"github.com/mna/juggler/internal/srvhandler"
	"github.com/mna/juggler/message"
	"github.com/mna/redisc"
)

var (
	allowEmptyProtoFlag = flag.Bool("allow-empty-subprotocol", false, "Allow empty subprotocol during handshake.")
	configFlag          = flag.String("config", "", "Path of the configuration `file`.")
	helpFlag            = flag.Bool("help", false, "Show help.")
	noLogFlag           = flag.Bool("L", false, "Disable logging.")
	portFlag            = flag.Int("port", 9000, "Server `port`.")
	redisAddrFlag       = flag.String("redis", ":6379", "Redis `address`.")
	redisClusterFlag    = flag.Bool("redis-cluster", false, "Use redis cluster.")
	redisMaxIdleFlag    = flag.Int("redis-max-idle", 0, "Maximum idle `connections`.")
)

func main() {
	flag.Parse()
	if *helpFlag {
		flag.Usage()
		return
	}

	conf, err := getConfigFromFile(*configFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load configuration file: %v\n", err)
		flag.Usage()
		os.Exit(1)
	}

	// TODO : support redis-cluster via the config file

	if err := checkRedisConfig(conf.Redis); err != nil {
		fmt.Fprintf(os.Stderr, "invalid redis configuration: %v\n", err)
		flag.Usage()
		os.Exit(3)
	}

	logFn := log.Printf
	if *noLogFlag {
		logFn = func(_ string, _ ...interface{}) {}
	}

	// create pool, brokers, server, upgrader, HTTP server
	var poolp, poolc redisbroker.Pool
	var dialp, dialc func() (redis.Conn, error)

	if conf.Redis.Addr != "" {
		createPoolFn := redisPoolCreateFunc(conf.Redis)
		if *redisClusterFlag {
			cluster, err := newRedisCluster(conf.Redis.Addr, createPoolFn)
			if err != nil {
				log.Fatalf("failed to connect to redis cluster: %v", err)
			}
			poolp, poolc = cluster, cluster
			dialp, dialc = cluster.Dial, cluster.Dial
			logFn("redis cluster configured on %s", conf.Redis.Addr)
		} else {
			pool, err := createPoolFn(conf.Redis.Addr)
			if err != nil {
				log.Fatalf("failed to connect to redis pool: %v", err)
			}
			poolp, poolc = pool, pool
			dialp, dialc = pool.Dial, pool.Dial
			logFn("redis pool configured on %s", conf.Redis.Addr)
		}
	} else {
		if *redisClusterFlag {
			fmt.Fprintln(os.Stderr, "cannot use redis cluster with different pubsub and caller configuration.")
			flag.Usage()
			os.Exit(4)
		}

		pp, err1 := redisPoolCreateFunc(conf.Redis.PubSub)(conf.Redis.PubSub.Addr)
		pc, err2 := redisPoolCreateFunc(conf.Redis.Caller)(conf.Redis.Caller.Addr)
		if err1 != nil || err2 != nil {
			err := err1
			if err1 == nil {
				err = err2
			}
			log.Fatalf("failed to connect to redis pool: %v", err)
		}
		poolp, poolc = pp, pc
		dialp, dialc = pp.Dial, pc.Dial
		logFn("redis pool configured on %s (pubsub) and %s (caller)", conf.Redis.PubSub.Addr, conf.Redis.Caller.Addr)
	}

	psb := newPubSubBroker(poolp, dialp, logFn)
	cb := newCallerBroker(conf.CallerBroker, poolc, dialc, logFn)

	srv := newServer(conf.Server, psb, cb, logFn)
	srv.Handler = newHandler(conf.Server, logFn)
	srv.Vars = expvar.NewMap("juggler")
	juggler.SlowProcessMsgThreshold = conf.Server.SlowProcessMsgThreshold

	upg := newUpgrader(conf.Server) // must be after newServer, for Subprotocols

	upgh := juggler.Upgrade(upg, srv)
	for _, p := range conf.Server.Paths {
		http.Handle(p, upgh)
	}

	httpSrv := newHTTPServer(conf.Server)

	logFn("listening for connections on %s", conf.Server.Addr)
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("ListenAndServe failed: %v", err)
	}
}

func newHandler(conf *Server, logFn func(string, ...interface{})) juggler.Handler {
	closeURI := conf.CloseURI
	panicURI := conf.PanicURI
	writeTimeout := conf.WriteTimeout

	process := juggler.HandlerFunc(func(ctx context.Context, c *juggler.Conn, m message.Msg) {
		if call, ok := m.(*message.Call); ok {
			switch call.Payload.URI {
			case closeURI:
				wsc := c.UnderlyingConn()

				deadline := time.Now().Add(writeTimeout)
				if writeTimeout == 0 {
					deadline = time.Time{}
				}

				if err := wsc.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"),
					deadline); err != nil {

					logFn("WriteControl failed: %v", err)
				}
				return

			case panicURI:
				panic("called panic URI")
			}
		}
		juggler.ProcessMsg(c, m)
	})

	chain := []juggler.Handler{process}
	if !*noLogFlag {
		chain = append([]juggler.Handler{srvhandler.LogMsg(logFn)}, chain...)
	}
	return srvhandler.PanicRecover(srvhandler.Chain(chain...), nil)
}

func newPubSubBroker(pool redisbroker.Pool, dial func() (redis.Conn, error), logFn func(string, ...interface{})) broker.PubSubBroker {
	return &redisbroker.Broker{
		Pool:    pool,
		Dial:    dial,
		LogFunc: logFn,
	}
}

func newCallerBroker(conf *CallerBroker, pool redisbroker.Pool, dial func() (redis.Conn, error), logFn func(string, ...interface{})) broker.CallerBroker {
	return &redisbroker.Broker{
		Pool:            pool,
		Dial:            dial,
		BlockingTimeout: conf.BlockingTimeout,
		CallCap:         conf.CallCap,
		LogFunc:         logFn,
	}
}

func isIn(list []string, v string) bool {
	for _, vv := range list {
		if v == vv {
			return true
		}
	}
	return false
}

func newUpgrader(conf *Server) *websocket.Upgrader {
	upg := &websocket.Upgrader{
		HandshakeTimeout: conf.HandshakeTimeout,
		ReadBufferSize:   conf.ReadBufferSize,
		WriteBufferSize:  conf.WriteBufferSize,
		Subprotocols:     juggler.Subprotocols,
	}

	if len(conf.WhitelistedOrigins) > 0 {
		oris := conf.WhitelistedOrigins
		upg.CheckOrigin = func(r *http.Request) bool {
			o := r.Header.Get("Origin")
			return isIn(oris, o)
		}
	}
	return upg
}

func newHTTPServer(conf *Server) *http.Server {
	return &http.Server{
		Addr:           conf.Addr,
		ReadTimeout:    conf.ReadTimeout,
		WriteTimeout:   conf.WriteTimeout,
		MaxHeaderBytes: conf.MaxHeaderBytes,
	}
}

func newServer(conf *Server, pubSub broker.PubSubBroker, caller broker.CallerBroker, logFn func(string, ...interface{})) *juggler.Server {
	if conf.AllowEmptySubprotocol {
		juggler.Subprotocols = append(juggler.Subprotocols, "")
	}

	cs := srvhandler.LogConn(logFn)
	if *noLogFlag {
		cs = nil
	}
	return &juggler.Server{
		ReadLimit:               conf.ReadLimit,
		ReadTimeout:             conf.ReadTimeout,
		WriteLimit:              conf.WriteLimit,
		WriteTimeout:            conf.WriteTimeout,
		AcquireWriteLockTimeout: conf.AcquireWriteLockTimeout,
		ConnState:               cs,
		PubSubBroker:            pubSub,
		CallerBroker:            caller,
	}
}

func newRedisCluster(addr string, createPool func(string, ...redis.DialOption) (*redis.Pool, error)) (*redisc.Cluster, error) {
	c := &redisc.Cluster{
		StartupNodes: []string{addr},
		CreatePool:   createPool,
	}
	err := c.Refresh()
	return c, err
}

func redisPoolCreateFunc(conf *Redis) func(string, ...redis.DialOption) (*redis.Pool, error) {
	return func(addr string, opts ...redis.DialOption) (*redis.Pool, error) {
		p := &redis.Pool{
			MaxIdle:     conf.MaxIdle,
			MaxActive:   conf.MaxActive,
			IdleTimeout: conf.IdleTimeout,
			Dial: func() (redis.Conn, error) {
				c, err := redis.Dial("tcp", addr, opts...)
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

		// test the connection so that it fails fast if redis is not available
		c := p.Get()
		defer c.Close()

		if _, err := c.Do("PING"); err != nil {
			return nil, err
		}
		return p, nil
	}
}
