package redisbroker

import (
	"encoding/json"
	"expvar"
	"sync"

	"github.com/PuerkitoBio/juggler/broker"
	"github.com/PuerkitoBio/juggler/message"
	"github.com/garyburd/redigo/redis"
)

var _ broker.PubSubConn = (*pubSubConn)(nil)

type pubSubConn struct {
	psc   redis.PubSubConn
	logFn func(string, ...interface{})
	vars  *expvar.Map

	// wmu controls writes (sub/unsub calls) to the connection.
	wmu sync.Mutex

	// once makes sure only the first call to Events starts the goroutine.
	once sync.Once
	evch chan *message.EvntPayload

	// errmu protects access to err.
	errmu sync.Mutex
	err   error
}

// Close closes the connection.
func (c *pubSubConn) Close() error {
	return c.psc.Close()
}

// Subscribe subscribes the redis connection to the channel, which may
// be a pattern.
func (c *pubSubConn) Subscribe(channel string, pattern bool) error {
	return c.subUnsub(channel, pattern, true)
}

// Unsubscribe unsubscribes the redis connection from the channel, which
// may be a pattern.
func (c *pubSubConn) Unsubscribe(channel string, pattern bool) error {
	return c.subUnsub(channel, pattern, false)
}

func (c *pubSubConn) subUnsub(ch string, pat bool, sub bool) error {
	var fn func(...interface{}) error
	switch {
	case pat && sub:
		fn = c.psc.PSubscribe
	case pat && !sub:
		fn = c.psc.PUnsubscribe
	case !pat && sub:
		fn = c.psc.Subscribe
	case !pat && !sub:
		fn = c.psc.Unsubscribe
	}

	c.wmu.Lock()
	err := fn(ch)
	c.wmu.Unlock()
	return err
}

// Events returns the stream of events from channels that the redis
// connection is subscribed to.
func (c *pubSubConn) Events() <-chan *message.EvntPayload {
	c.once.Do(func() {
		c.evch = make(chan *message.EvntPayload)
		go c.listen()
	})

	return c.evch
}

func (c *pubSubConn) listen() {
	defer close(c.evch)

	wg := sync.WaitGroup{}
	for {
		switch v := c.psc.Receive().(type) {
		case redis.Message:
			wg.Add(1)
			go c.sendEvent(v.Channel, "", v.Data, &wg)

		case redis.PMessage:
			wg.Add(1)
			go c.sendEvent(v.Channel, v.Pattern, v.Data, &wg)

		case error:
			// possibly because the pub-sub connection was closed, but
			// in any case, the pub-sub is now broken, terminate the
			// loop.
			c.errmu.Lock()
			c.err = v
			c.errmu.Unlock()
			wg.Wait()
			return
		}
	}
}

func (c *pubSubConn) sendEvent(channel, pattern string, pld []byte, wg *sync.WaitGroup) {
	defer wg.Done()

	ep, err := newEvntPayload(channel, pattern, pld)
	if err != nil {
		if c.vars != nil {
			c.vars.Add("FailedEvntPayloadUnmarshals", 1)
		}
		logf(c.logFn, "Events: failed to unmarshal event payload: %v", err)
		return
	}
	c.evch <- ep
}

func newEvntPayload(channel, pattern string, pld []byte) (*message.EvntPayload, error) {
	var pp message.PubPayload
	if err := json.Unmarshal(pld, &pp); err != nil {
		return nil, err
	}
	ep := &message.EvntPayload{
		MsgUUID: pp.MsgUUID,
		Channel: channel,
		Pattern: pattern,
		Args:    pp.Args,
	}
	return ep, nil
}

// EventsErr returns the error that caused the events channel to close.
func (c *pubSubConn) EventsErr() error {
	c.errmu.Lock()
	err := c.err
	c.errmu.Unlock()
	return err
}
