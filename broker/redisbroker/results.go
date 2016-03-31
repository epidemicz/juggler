package redisbroker

import (
	"expvar"
	"fmt"
	"sync"
	"time"

	"github.com/PuerkitoBio/juggler/broker"
	"github.com/PuerkitoBio/juggler/message"
	"github.com/garyburd/redigo/redis"
	"github.com/pborman/uuid"
)

var _ broker.ResultsConn = (*resultsConn)(nil)

type resultsConn struct {
	c        redis.Conn
	pool     Pool
	connUUID uuid.UUID
	timeout  time.Duration
	logFn    func(string, ...interface{})
	vars     *expvar.Map

	// once makes sure only the first call to Results starts the goroutine.
	once sync.Once
	ch   chan *message.ResPayload

	// errmu protects access to err.
	errmu sync.Mutex
	err   error
}

// Close closes the connection.
func (c *resultsConn) Close() error {
	return c.c.Close()
}

// ResultsErr returns the error that caused the Results channel to close.
func (c *resultsConn) ResultsErr() error {
	c.errmu.Lock()
	err := c.err
	c.errmu.Unlock()
	return err
}

// Results returns a stream of call results for the connUUID specified when
// creating the resultsConn.
func (c *resultsConn) Results() <-chan *message.ResPayload {
	c.once.Do(func() {
		c.ch = make(chan *message.ResPayload)

		// compute key and timeout
		key := fmt.Sprintf(resKey, c.connUUID)
		to := int(c.timeout / time.Second)

		// make connection cluster-aware if running in a cluster
		rc := clusterifyConn(c.c, key)

		go c.pollResults(rc, key, to)
	})

	return c.ch
}

func (c *resultsConn) pollResults(pollConn redis.Conn, key string, timeout int) {
	defer close(c.ch)

	wg := sync.WaitGroup{}
	for {
		// BRPOP returns array with [0]: key name, [1]: payload.
		v, err := redis.Values(pollConn.Do("BRPOP", key, timeout))
		if err != nil {
			if err == redis.ErrNil {
				// no available value
				continue
			}

			// possibly a closed connection, in any case stop
			// the loop.
			c.errmu.Lock()
			c.err = err
			c.errmu.Unlock()
			wg.Wait()
			return
		}

		wg.Add(1)
		go c.sendResult(v, &wg)
	}
}

// receives the raw value v retured from BRPOP.
func (c *resultsConn) sendResult(v []interface{}, wg *sync.WaitGroup) {
	defer wg.Done()

	// unmarshal the payload
	var rp message.ResPayload
	if err := unmarshalBRPOPValue(&rp, v); err != nil {
		if c.vars != nil {
			c.vars.Add("FailedResPayloadUnmarshals", 1)
		}
		logf(c.logFn, "Results: BRPOP failed to unmarshal result payload: %v", err)
		return
	}

	// check if call is expired
	k := fmt.Sprintf(resTimeoutKey, rp.ConnUUID, rp.MsgUUID)

	rc := c.pool.Get()
	defer rc.Close()
	rc = clusterifyConn(rc, k)

	pttl, err := redis.Int(delAndPTTLScript.Do(rc, k))
	if err != nil {
		if c.vars != nil {
			c.vars.Add("FailedPTTLResults", 1)
		}
		logf(c.logFn, "Results: DEL/PTTL failed: %v", err)
		return
	}
	if pttl <= 0 {
		if c.vars != nil {
			c.vars.Add("ExpiredResults", 1)
		}
		logf(c.logFn, "Results: message %v expired, dropping call", rp.MsgUUID)
		return
	}

	c.ch <- &rp
	if c.vars != nil {
		c.vars.Add("Results", 1)
	}
}
