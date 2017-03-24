// Package client implements a juggler client. Once a Client is
// returned via a call to Dial or New, it can be used to make calls to an
// RPC function identified by a URI, to subscribe to and unsubscribe
// from pub-sub channels, and to publish events to a pub-sub channel.
//
// Received replies and pub-sub events are handled by a Handler.
// Each received message is sent to the Handler in a separate
// goroutine. RPC calls that did not return a result before the
// call timeout expired generate a custom ExpMsg message type, so an
// RPC call that succeeded (that is, for which the server returned
// an ACK message, not a NACK) either generates a RES or an EXP,
// but never both or none.
//
package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/context"

	"github.com/mna/juggler/broker"
	"github.com/mna/juggler/internal/wswriter"
	"github.com/mna/juggler/message"
	"github.com/gorilla/websocket"
	"github.com/pborman/uuid"
)

// Client is a juggler client based on a websocket connection. It is
// used to send and receive messages to and from a juggler server.
type Client struct {
	conn *websocket.Conn

	// options
	callTimeout             time.Duration
	handler                 Handler
	readTimeout             time.Duration
	writeTimeout            time.Duration
	acquireWriteLockTimeout time.Duration
	writeLimit              int64

	// stop signal for expiration goroutines, signals close of client
	stop chan struct{}

	wmu     chan struct{} // exclusive write lock
	mu      sync.Mutex    // lock access to results map and err field
	results map[string]struct{}
	err     error
}

// New creates a juggler client using the provided websocket
// connection. Received messages are sent to the handler set by
// the SetHandler option.
func New(conn *websocket.Conn, opts ...Option) *Client {
	// wmu is the write lock, used as mutex so it can be select'ed upon.
	// start with an available slot (initialize with a sent value).
	wmu := make(chan struct{}, 1)
	wmu <- struct{}{}

	c := &Client{
		conn:    conn,
		stop:    make(chan struct{}),
		wmu:     wmu,
		results: make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	go c.handleMessages()
	return c
}

func (c *Client) handleMessages() {
	defer close(c.stop)

	for {
		_, r, err := c.conn.NextReader()
		if err != nil {
			c.mu.Lock()
			if c.err == nil {
				c.err = err
			}
			c.mu.Unlock()
			return
		}

		m, err := message.UnmarshalResponse(r)
		if err != nil {
			continue
		}

		switch m := m.(type) {
		case *message.Res:
			// got the result, do not trigger an expired message
			if ok := c.deletePending(m.Payload.For.String()); !ok {
				// if an expired message got here first, then drop the
				// result, client treated this call as expired already.
				continue
			}

		case *message.Nack:
			if m.Payload.ForType == message.CallMsg {
				// won't get any result for this call (unless already expired)
				c.deletePending(m.Payload.For.String())
			}
		}

		go c.handler.Handle(context.Background(), m)
	}
}

// Dial is a helper function to create a Client connected to urlStr using
// the provided *websocket.Dialer and request headers. If the connection
// succeeds, it returns the initialized client, otherwise it returns an
// error. It does not allow handling redirections and such, for a better
// control over the connection, directly use the *websocket.Dialer and
// create the client once the connection is established, using New.
//
// The Dialer's Subprotocols field should be set to one of (or any/all of)
// juggler.Subprotocol. To limit the client to a restricted subset of
// messages, set the Juggler-Allowed-Messages header on reqHeader
// (see the documentation of juggler.Upgrade for details).
func Dial(d *websocket.Dialer, urlStr string, reqHeader http.Header, opts ...Option) (*Client, error) {
	conn, _, err := d.Dial(urlStr, reqHeader)
	if err != nil {
		return nil, err
	}
	return New(conn, opts...), nil
}

// Close closes the connection. No more messages will be received.
func (c *Client) Close() error {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()

	// closing the websocket connection causes the NextReader
	// call in handleMessages to fail, closing c.stop.
	err2 := c.conn.Close()
	<-c.stop

	if err == nil {
		// if c.err is nil, store the close error
		err = err2
		c.mu.Lock()
		if err2 != nil {
			c.err = err2
		} else {
			c.err = errors.New("closed connection")
		}
		c.mu.Unlock()
	}
	return err
}

// CloseNotify returns a channel that is closed when the client is
// closed.
func (c *Client) CloseNotify() <-chan struct{} {
	return c.stop
}

// UnderlyingConn returns the underlying websocket connection used by the
// client. Care should be taken when using the websocket connection
// directly, as it may interfere with the normal behaviour of the client.
func (c *Client) UnderlyingConn() *websocket.Conn {
	return c.conn
}

// Call makes a call request to the server for the remote procedure
// identified by uri. The v value is marshaled as JSON and sent as
// the parameters to the remote procedure. If timeout is > 0, it is used
// as the call-specific timeout, otherwise Client.CallTimeout is used.
//
// It returns the UUID of the call message on success, or an error if
// the call request could not be sent to the server.
func (c *Client) Call(uri string, v interface{}, timeout time.Duration) (uuid.UUID, error) {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}

	if timeout <= 0 {
		timeout = c.callTimeout
	}
	m, err := message.NewCall(uri, v, timeout)
	if err != nil {
		return nil, err
	}
	if err := c.doWrite(m); err != nil {
		return nil, err
	}

	// add the expected result
	c.addPending(m.UUID().String())

	go c.handleExpiredCall(m, timeout)
	return m.UUID(), nil
}

func (c *Client) handleExpiredCall(m *message.Call, timeout time.Duration) {
	// wait for the timeout
	if timeout <= 0 {
		timeout = broker.DefaultCallTimeout
	}
	select {
	case <-c.stop:
		return
	case <-time.After(timeout):
	}

	// check if still waiting for a result
	if ok := c.deletePending(m.UUID().String()); ok {
		// if so, send an Exp message
		exp := newExp(m)
		go c.handler.Handle(context.Background(), exp)
	}
}

// add a pending call.
func (c *Client) addPending(key string) {
	c.mu.Lock()
	c.results[key] = struct{}{}
	c.mu.Unlock()
}

// delete the pending call, returning true if it was still pending.
func (c *Client) deletePending(key string) bool {
	c.mu.Lock()
	_, ok := c.results[key]
	delete(c.results, key)
	c.mu.Unlock()

	return ok
}

// Sub makes a subscription request to the server for the specified
// channel, which is treated as a pattern if pattern is true. It
// returns the UUID of the sub message on success, or an error if
// the request could not be sent to the server.
func (c *Client) Sub(channel string, pattern bool) (uuid.UUID, error) {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}

	m := message.NewSub(channel, pattern)
	if err := c.doWrite(m); err != nil {
		return nil, err
	}
	return m.UUID(), nil
}

// Unsb makes an unsubscription request to the server for the specified
// channel, which is treated as a pattern if pattern is true. It
// returns the UUID of the unsb message on success, or an error if
// the request could not be sent to the server.
func (c *Client) Unsb(channel string, pattern bool) (uuid.UUID, error) {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}

	m := message.NewUnsb(channel, pattern)
	if err := c.doWrite(m); err != nil {
		return nil, err
	}
	return m.UUID(), nil
}

// Pub makes a publish request to the server on the specified channel.
// The v value is marshaled as JSON and sent as event payload. It returns
// the UUID of the pub message on success, or an error if the request could
// not be sent to the server.
func (c *Client) Pub(channel string, v interface{}) (uuid.UUID, error) {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}

	m, err := message.NewPub(channel, v)
	if err != nil {
		return nil, err
	}
	if err := c.doWrite(m); err != nil {
		return nil, err
	}
	return m.UUID(), nil
}

// doWrite calls writeMsg and handles errors so that the connection is
// marked as failed if the error is fatal.
func (c *Client) doWrite(m message.Msg) error {
	err := c.writeMsg(m)
	switch err {
	case wswriter.ErrWriteLimitExceeded,
		wswriter.ErrWriteLockTimeout:
		c.mu.Lock()
		if c.err == nil {
			c.err = err
		}
		c.mu.Unlock()
	}
	return err
}

func (c *Client) writeMsg(m message.Msg) error {
	w := wswriter.Exclusive(c.conn, c.wmu, c.acquireWriteLockTimeout, c.writeTimeout)
	defer w.Close()

	lw := io.Writer(w)
	if l := c.writeLimit; l > 0 {
		lw = wswriter.Limit(w, l)
	}
	return json.NewEncoder(lw).Encode(m)
}

// Handler defines the method required to handle a message received
// from the server.
type Handler interface {
	Handle(context.Context, message.Msg)
}

// HandlerFunc is a function that implements the Handler interface.
type HandlerFunc func(context.Context, message.Msg)

// Handle implements Handler for a HandlerFunc. It calls fn
// with the parameters.
func (fn HandlerFunc) Handle(ctx context.Context, m message.Msg) {
	fn(ctx, m)
}

// Option sets an option on the Client.
type Option func(*Client)

// SetCallTimeout sets the time to wait for the result of a call request.
// The zero value uses the default timeout of the server. Per-call
// timeouts can also be specified, see Client.Call.
func SetCallTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		c.callTimeout = timeout
	}
}

// SetHandler sets the handler that is called with each message
// received from the server. Each invocation runs in its own
// goroutine, so proper synchronization must be used when accessing
// shared data.
func SetHandler(h Handler) Option {
	return func(c *Client) {
		c.handler = h
	}
}

// SetReadTimeout sets the read timeout of the connection.
func SetReadTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		c.readTimeout = timeout
	}
}

// SetWriteTimeout sets the write timeout of the connection.
func SetWriteTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		c.writeTimeout = timeout
	}
}

// SetAcquireWriteLockTimeout sets the timeout to acquire the exclusive
// write lock. If a lock cannot be acquired before the timeout, the connection
// is marked as failed and should be closed.
func SetAcquireWriteLockTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		c.acquireWriteLockTimeout = timeout
	}
}

// SetReadLimit sets the limit in bytes of messages read from the connection.
// If a message exceeds the limit, the connection is marked as failed and
// should be closed.
func SetReadLimit(limit int64) Option {
	return func(c *Client) {
		c.conn.SetReadLimit(limit)
	}
}

// SetWriteLimit sets the limit in bytes of messages sent on the connection.
// If a message exceeds the limit, the connection is marked as failed and
// should be closed.
func SetWriteLimit(limit int64) Option {
	return func(c *Client) {
		c.writeLimit = limit
	}
}

// Exp is an expired call message. It is never sent over the network, but
// it is raised by the client for itself, when the timeout for a call
// result has expired. As such, its message type returns false for
// both IsRead and IsWrite.
type Exp struct {
	message.Meta `json:"meta"`
	Payload      struct {
		For  uuid.UUID       `json:"for"`           // no ForType, because always CALL
		URI  string          `json:"uri,omitempty"` // URI of the CALL
		Args json.RawMessage `json:"args"`
	} `json:"payload"`
}

// ExpMsg is the message type of the call expiration message.
var ExpMsg = message.Register("EXP")

// newExp creates a new expired message for the provided call message.
func newExp(m *message.Call) *Exp {
	exp := &Exp{
		Meta: message.NewMeta(ExpMsg),
	}
	exp.Payload.For = m.UUID()
	exp.Payload.URI = m.Payload.URI
	exp.Payload.Args = m.Payload.Args
	return exp
}
