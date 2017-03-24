package juggler

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/net/context"

	"github.com/mna/juggler/broker"
	"github.com/mna/juggler/internal/wswriter"
	"github.com/mna/juggler/message"
	"github.com/gorilla/websocket"
	"github.com/pborman/uuid"
)

// ConnState represents the possible states of a connection.
type ConnState int

// The list of possible connection states.
const (
	Unknown ConnState = iota
	Accepting
	Connected
	Closed
)

// Conn is a juggler connection. Each connection is identified by
// a UUID and has an underlying websocket connection. It is safe to
// call methods on a Conn concurrently, but the fields should be
// treated as read-only.
type Conn struct {
	// UUID is the unique identifier of the connection.
	UUID uuid.UUID

	// CloseErr is the error, if any, that caused the connection
	// to close. Must only be accessed after the close notification
	// has been received (i.e. after a <-conn.CloseNotify()).
	CloseErr error

	// the underlying websocket connection.
	wsConn *websocket.Conn
	// allowed types of messages from the client (empty means any)
	allowedMsgs []message.Type

	wmu  chan struct{} // exclusive write lock
	srv  *Server
	psc  broker.PubSubConn  // single pub-sub-dedicated broker connection
	resc broker.ResultsConn // single results-dedicated broker connection

	// ensure the kill channel can only be closed once
	closeOnce sync.Once
	kill      chan struct{}
}

func newConn(c *websocket.Conn, srv *Server, allowedMsgs ...message.Type) *Conn {
	// wmu is the write lock, used as mutex so it can be select'ed upon.
	// start with an available slot (initialize with a sent value).
	wmu := make(chan struct{}, 1)
	wmu <- struct{}{}

	return &Conn{
		UUID:        uuid.NewRandom(),
		wsConn:      c,
		allowedMsgs: allowedMsgs,
		wmu:         wmu,
		srv:         srv,
		kill:        make(chan struct{}),
	}
}

// UnderlyingConn returns the underlying websocket connection. Care
// should be taken when using the websocket connection directly,
// as it may interfere with the normal juggler connection behaviour.
func (c *Conn) UnderlyingConn() *websocket.Conn {
	return c.wsConn
}

// CloseNotify returns a signal channel that is closed when the
// Conn is closed.
func (c *Conn) CloseNotify() <-chan struct{} {
	return c.kill
}

// LocalAddr returns the local network address.
func (c *Conn) LocalAddr() net.Addr {
	return c.wsConn.LocalAddr()
}

// RemoteAddr returns the remote network address.
func (c *Conn) RemoteAddr() net.Addr {
	return c.wsConn.RemoteAddr()
}

// Subprotocol returns the negotiated protocol for the connection.
func (c *Conn) Subprotocol() string {
	return c.wsConn.Subprotocol()
}

// Close closes the connection, setting err as CloseErr to identify
// the reason of the close. It does not send a websocket close message,
// nor does it close the underlying websocket connection.
// As with all Conn methods, it is safe to call concurrently, but
// only the first call will set the CloseErr field to err.
func (c *Conn) Close(err error) {
	c.closeOnce.Do(func() {
		c.CloseErr = err
		if c.psc != nil {
			c.psc.Close()
		}
		if c.resc != nil {
			c.resc.Close()
		}
		close(c.kill)
	})
}

// Writer returns an io.WriteCloser that can be used to send a
// message on the connection. Only one writer can be active at
// any moment for a given connection, so the returned writer
// will acquire a lock on the first call to Write, and will
// release it only when Close is called. The timeout controls
// the time to wait to acquire the lock on the first call to
// Write. If the lock cannot be acquired within that time,
// an error is returned and no write is performed.
//
// It is possible to enter a deadlock state if Writer is called
// with no timeout, an initial Write is executed, and Writer is
// called again from the same goroutine, without a timeout.
// To avoid this, make sure each goroutine closes the Writer
// before asking for another one, and ideally always use a timeout.
//
// The returned writer itself is not safe for concurrent use, but
// as all Conn methods, Writer can be called concurrently.
func (c *Conn) Writer(timeout time.Duration) io.WriteCloser {
	return wswriter.Exclusive(
		c.wsConn,
		c.wmu,
		timeout,
		c.srv.WriteTimeout,
	)
}

// Send sends the message to the client. It calls the server's
// Handler if any, or ProcessMsg if nil.
func (c *Conn) Send(m message.Msg) {
	if h := c.srv.Handler; h != nil {
		h.Handle(context.Background(), c, m)
	} else {
		ProcessMsg(c, m)
	}
}

// results is the loop that looks for call results, started in its own
// goroutine.
func (c *Conn) results() {
	if c.srv.Vars != nil {
		c.srv.Vars.Add("TotalConnGoros", 1)
		c.srv.Vars.Add("ActiveConnGoros", 1)
		defer c.srv.Vars.Add("ActiveConnGoros", -1)
	}

	ch := c.resc.Results()
	for res := range ch {
		c.Send(message.NewRes(res))
	}

	// results loop was stopped, the connection should be closed if it
	// isn't already.
	c.Close(c.resc.ResultsErr())
}

// pubSub is the loop that receives events that the connection is subscribed
// to, started in its own goroutine.
func (c *Conn) pubSub() {
	if c.srv.Vars != nil {
		c.srv.Vars.Add("TotalConnGoros", 1)
		c.srv.Vars.Add("ActiveConnGoros", 1)
		defer c.srv.Vars.Add("ActiveConnGoros", -1)
	}

	ch := c.psc.Events()
	for ev := range ch {
		c.Send(message.NewEvnt(ev))
	}

	// pubsub loop was stopped, the connection should be closed if it
	// isn't already.
	c.Close(c.psc.EventsErr())
}

// receive is the read loop, started in its own goroutine.
func (c *Conn) receive() {
	if c.srv.Vars != nil {
		c.srv.Vars.Add("TotalConnGoros", 1)
		c.srv.Vars.Add("ActiveConnGoros", 1)
		defer c.srv.Vars.Add("ActiveConnGoros", -1)
	}

	for {
		c.wsConn.SetReadDeadline(time.Time{})

		// NextReader returns with an error once a connection is closed,
		// so this loop doesn't need to check the c.kill channel.
		mt, r, err := c.wsConn.NextReader()
		if err != nil {
			c.Close(err)
			return
		}
		if mt != websocket.TextMessage {
			c.Close(fmt.Errorf("invalid websocket message type: %d", mt))
			return
		}
		if to := c.srv.ReadTimeout; to > 0 {
			c.wsConn.SetReadDeadline(time.Now().Add(to))
		}

		m, err := message.UnmarshalRequest(r, c.allowedMsgs...)
		if err != nil {
			c.Close(err)
			return
		}

		if h := c.srv.Handler; h != nil {
			h.Handle(context.Background(), c, m)
		} else {
			ProcessMsg(c, m)
		}
	}
}
