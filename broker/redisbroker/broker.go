// Package redisbroker implements a juggler broker using redis
// as backend. RPC calls and results are stored in redis lists
// and queried via the BRPOP command, while pub-sub events
// are handled using redis' built-in pub-sub support.
//
// Call timeouts are handled by an expiring key associated
// with each call request, and in a similar way for results.
// Keys are named in such a way that the call request list
// and associated expiring keys are in the same hash slot,
// and the same is true for results and their expiring key,
// so that using a redis cluster is supported. The call
// requests are hashed on the call URI, and the results
// are hashed on the calling connection's UUID.
//
// If an RPC URI is much more sollicitated than others,
// it can be spread over multiple URIs using
// "RPC_URI_%d" where %d is e.g. a number from 1 to 100.
// Clients that need to call this function can use a random
// over that range to spread the load over different cluster
// nodes, or a server handler can alter the URI to achieve
// that result without impacting clients.
//
package redisbroker

import (
	"encoding/json"
	"expvar"
	"fmt"
	"log"
	"time"

	"github.com/mna/juggler/broker"
	"github.com/mna/juggler/message"
	"github.com/mna/redisc"
	"github.com/garyburd/redigo/redis"
	"github.com/pborman/uuid"
)

var (
	// static check that *Broker implements all the broker interfaces
	_ broker.CallerBroker = (*Broker)(nil)
	_ broker.CalleeBroker = (*Broker)(nil)
	_ broker.PubSubBroker = (*Broker)(nil)
)

// DiscardLog is a no-op logging function that can be used as Broker.LogFunc
// to disable logging.
var DiscardLog = func(_ string, _ ...interface{}) {}

// Pool defines the methods required for a redis pool that provides
// a method to get a connection and to release the pool's resources.
type Pool interface {
	// Get returns a redis connection.
	Get() redis.Conn

	// Close releases the resources used by the pool.
	Close() error
}

// Broker is a broker that provides the methods to
// interact with redis using the juggler protocol.
type Broker struct {
	// prevent unkeyed literals
	_ struct{}

	// Pool is the redis pool or redisc cluster to use to get
	// short-lived connections.
	Pool Pool

	// Dial is the function to call to get a non-pooled, long-lived
	// redis connection. Typically, it can be set to redis.Pool.Dial
	// or redisc.Cluster.Dial.
	Dial func() (redis.Conn, error)

	// BlockingTimeout is the time to wait for a value on calls to
	// BRPOP before trying again. The default of 0 means no timeout.
	BlockingTimeout time.Duration

	// LogFunc is the logging function to use. If nil, log.Printf
	// is used. It can be set to DiscardLog to disable logging.
	LogFunc func(string, ...interface{})

	// CallCap is the capacity of the CALL queue per URI. If it is
	// exceeded for a given URI, subsequent Broker.Call calls for that
	// URI will fail with an error. The default of 0 means no limit.
	CallCap int

	// ResultCap is the capacity of the RES queue per connection UUID.
	// If it is exceeded for a given connection, Broker.Result calls
	// for that connection will fail with an error. The default of 0
	// means no limit.
	ResultCap int

	// Vars can be set to an *expvar.Map to collect metrics about the
	// broker. It should be set before starting to make calls with the
	// broker.
	Vars *expvar.Map
}

// script to store the call request or call result along with
// its expiration information.
var callOrResScript = redis.NewScript(2, `
	redis.call("SET", KEYS[1], ARGV[1], "PX", tonumber(ARGV[1]))
	local res = redis.call("LPUSH", KEYS[2], ARGV[2])
	local limit = tonumber(ARGV[3])
	if res > limit and limit > 0 then
		local diff = res - limit
		redis.call("LTRIM", KEYS[2], diff, limit + diff)
		return redis.error_reply("list capacity exceeded")
	end
	return res
`)

const (
	// redis cluster-compliant keys, so that both keys are in the same slot
	callKey        = "juggler:calls:{%s}"            // 1: URI
	callTimeoutKey = "juggler:calls:timeout:{%s}:%s" // 1: URI, 2: mUUID

	// redis cluster-compliant keys, so that both keys are in the same slot
	resKey        = "juggler:results:{%s}"            // 1: cUUID
	resTimeoutKey = "juggler:results:timeout:{%s}:%s" // 1: cUUID, 2: mUUID
)

// Call registers a call request in the broker.
func (b *Broker) Call(cp *message.CallPayload, timeout time.Duration) error {
	k1 := fmt.Sprintf(callTimeoutKey, cp.URI, cp.MsgUUID)
	k2 := fmt.Sprintf(callKey, cp.URI)
	return registerCallOrRes(b.Pool, cp, timeout, b.CallCap, k1, k2)
}

// Result registers a call result in the broker.
func (b *Broker) Result(rp *message.ResPayload, timeout time.Duration) error {
	k1 := fmt.Sprintf(resTimeoutKey, rp.ConnUUID, rp.MsgUUID)
	k2 := fmt.Sprintf(resKey, rp.ConnUUID)
	return registerCallOrRes(b.Pool, rp, timeout, b.ResultCap, k1, k2)
}

func registerCallOrRes(pool Pool, pld interface{}, timeout time.Duration, cap int, k1, k2 string) error {
	p, err := json.Marshal(pld)
	if err != nil {
		return err
	}

	rc := pool.Get()
	defer rc.Close()

	// turn it into a cluster-aware RetryConn if running in a cluster
	rc = clusterifyConn(rc, k1, k2)

	to := int(timeout / time.Millisecond)
	if to == 0 {
		to = int(broker.DefaultCallTimeout / time.Millisecond)
	}

	_, err = callOrResScript.Do(rc,
		k1,  // key[1] : the SET key with expiration
		k2,  // key[2] : the LIST key
		to,  // argv[1] : the timeout in milliseconds
		p,   // argv[2] : the call payload
		cap, // argv[3] : the LIST capacity
	)
	return err
}

// Publish publishes an event to a channel.
func (b *Broker) Publish(channel string, pp *message.PubPayload) error {
	p, err := json.Marshal(pp)
	if err != nil {
		return err
	}

	rc := b.Pool.Get()
	defer rc.Close()

	// force selection of a random node (otherwise it would use
	// the node of the hash of the channel - which may hit the
	// same node over and over again if there are few channels).
	if bc, ok := rc.(binder); ok {
		// ignore the error, if it fails, use the connection as-is.
		// Bind without a key selects a random node.
		bc.Bind()
	}
	_, err = rc.Do("PUBLISH", channel, p)
	return err
}

// NewPubSubConn returns a new pub-sub connection that can be used
// to subscribe to and unsubscribe from channels, and to process
// incoming events.
func (b *Broker) NewPubSubConn() (broker.PubSubConn, error) {
	rc, err := b.Dial()
	if err != nil {
		return nil, err
	}
	return &pubSubConn{
		psc:   redis.PubSubConn{Conn: rc},
		logFn: b.LogFunc,
		vars:  b.Vars,
	}, nil
}

// NewCallsConn returns a new calls connection that can be used
// to process the call requests for the specified URIs.
func (b *Broker) NewCallsConn(uris ...string) (broker.CallsConn, error) {
	rc, err := b.Dial()
	if err != nil {
		return nil, err
	}
	return &callsConn{
		c:       rc,
		pool:    b.Pool,
		uris:    uris,
		vars:    b.Vars,
		timeout: b.BlockingTimeout,
		logFn:   b.LogFunc,
	}, nil
}

// NewResultsConn returns a new results connection that can be used
// to process the call results for the specified connection UUID.
func (b *Broker) NewResultsConn(connUUID uuid.UUID) (broker.ResultsConn, error) {
	rc, err := b.Dial()
	if err != nil {
		return nil, err
	}
	return &resultsConn{
		c:        rc,
		pool:     b.Pool,
		connUUID: connUUID,
		vars:     b.Vars,
		timeout:  b.BlockingTimeout,
		logFn:    b.LogFunc,
	}, nil
}

const (
	// TODO : maybe make that customizable, not super critical.
	clusterConnMaxAttempts   = 4
	clusterConnTryAgainDelay = 100 * time.Millisecond
)

type binder interface {
	Bind(...string) error
}

func clusterifyConn(rc redis.Conn, keys ...string) redis.Conn {
	// if it implements Bind, call it and make it a RetryConn so
	// that it follows redirections in a cluster.
	if bc, ok := rc.(binder); ok {
		// if Bind fails, go on with the call as usual, but if it
		// succeeds, try to turn it into a RetryConn.
		if err := bc.Bind(keys...); err == nil {
			retry, err := redisc.RetryConn(rc, clusterConnMaxAttempts, clusterConnTryAgainDelay)
			// again, if it fails, ignore and go on with the normal conn,
			// but if it succeds, replace the conn with this one.
			if err == nil {
				rc = retry
			}
		}
	}
	return rc
}

func logf(fn func(string, ...interface{}), f string, args ...interface{}) {
	if fn != nil {
		fn(f, args...)
	} else {
		log.Printf(f, args...)
	}
}
