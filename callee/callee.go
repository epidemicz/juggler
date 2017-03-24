// Package callee implements the Callee type to use to listen for
// and process RPC call requests. A callee listens to some URIs using
// a broker.CalleeBroker, and stores the result of the calls so that
// the broker can send it back to the calling client.
package callee

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/mna/juggler/broker"
	"github.com/mna/juggler/message"
)

// ErrCallExpired is returned when a call is processed but the
// call timeout is exceeded, meaning that the client is no longer
// expecting the result. The result is dropped and this error is
// returned from InvokeAndStoreResult.
var ErrCallExpired = errors.New("juggler/callee: call expired")

// Thunk is the function signature for functions that handle calls
// to a URI. Generally, it should be used to decode the arguments
// to the type expected by the actual underlying function, call that
// strongly-typed function, and transfer the results back in the
// generic empty interface.
type Thunk func(*message.CallPayload) (interface{}, error)

// Callee is a peer that handles call requests for some URIs.
type Callee struct {
	// prevent unkeyed literals
	_ struct{}

	// Broker is the callee broker to use to listen for call requests
	// and to store results.
	Broker broker.CalleeBroker
}

// InvokeAndStoreResult processes the provided call payload by calling
// fn and storing the result so that it can be sent back to the caller.
// If the call timeout is exceeded, the result is dropped and
// ErrCallExpired is returned.
func (c *Callee) InvokeAndStoreResult(cp *message.CallPayload, fn Thunk) error {
	ttl := cp.TTLAfterRead
	start := time.Now()

	v, err := fn(cp)
	if remain := ttl - time.Now().Sub(start); remain > 0 {
		// register the result
		return c.storeResult(cp, v, err, remain)
	}
	return ErrCallExpired
}

// Listen is a helper method that listens for call requests for the
// requested URIs and calls the corresponding Thunk to execute the
// request. The m map has URIs as keys, and the associated Thunk
// function as value. If a redis cluster is used, all URIs in m
// must belong to the same hash slot.
//
// The method implements a single-producer, single-consumer helper,
// where a single redis connection is used to listen for call requests
// on the URIs, and for each request, a single goroutine executes
// the calls and stores the results. If there's an error when storing
// the result, that error is ignored and the next request is processed.
// More advanced concurrency patterns and error handling can be
// implemented using Callee.Broker.Calls directly, and starting multiple
// consumer goroutines reading from the same calls channel and calling
// InvokeAndStoreResult to process each call request.
//
// The function blocks until the call request loop exits. It returns
// the error that caused the loop to stop, or the error to initiate
// the connection to the broker.
func (c *Callee) Listen(m map[string]Thunk) error {
	if len(m) == 0 {
		return nil
	}

	uris := make([]string, 0, len(m))
	for k := range m {
		uris = append(uris, k)
	}
	conn, err := c.Broker.NewCallsConn(uris...)
	if err != nil {
		return err
	}
	defer conn.Close()

	for cp := range conn.Calls() {
		// errors are ignored, use InvokeAndStoreResult directly to handle them.
		c.InvokeAndStoreResult(cp, m[cp.URI])
	}
	return conn.CallsErr()
}

func (c *Callee) storeResult(cp *message.CallPayload, v interface{}, e error, timeout time.Duration) error {
	// if there's an error, that's what gets stored
	if e != nil {
		if ms, ok := e.(json.Marshaler); ok {
			v = ms
		} else {
			var er message.ErrResult
			er.Error.Message = e.Error()
			v = er
		}
	}

	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	rp := &message.ResPayload{
		ConnUUID: cp.ConnUUID,
		MsgUUID:  cp.MsgUUID,
		URI:      cp.URI,
		Args:     b,
	}
	return c.Broker.Result(rp, timeout)
}
