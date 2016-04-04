// Package srvhandler implements server handlers used by the juggler-server
// command and various tests.
package srvhandler

import (
	"expvar"
	"fmt"

	"github.com/PuerkitoBio/juggler"
	"github.com/PuerkitoBio/juggler/message"
	"golang.org/x/net/context"
)

// Chain returns a juggler.Handler that calls the provided handlers
// in order, one after the other.
func Chain(hs ...juggler.Handler) juggler.Handler {
	return juggler.HandlerFunc(func(ctx context.Context, c *juggler.Conn, m message.Msg) {
		for _, h := range hs {
			h.Handle(ctx, c, m)
		}
	})
}

// PanicRecover returns a juggler.Handler that recovers from panics that
// may happen in h. The connection is closed on a panic. If a non-nil
// vars is passed as parameter, the RecoveredPanics counter is incremented
// for each panic.
func PanicRecover(h juggler.Handler, vars *expvar.Map) juggler.Handler {
	return juggler.HandlerFunc(func(ctx context.Context, c *juggler.Conn, m message.Msg) {
		defer func() {
			if e := recover(); e != nil {
				if vars != nil {
					vars.Add("RecoveredPanics", 1)
				}

				var err error
				switch e := e.(type) {
				case error:
					err = e
				default:
					err = fmt.Errorf("%v", e)
				}
				c.Close(err)
			}
		}()
		h.Handle(ctx, c, m)
	})
}

// LogConn returns a function compatible with the Server.ConnState field
// type that logs connections and disconnections to the provided logger
// function. It is not a juggler.Handler.
func LogConn(logFn func(string, ...interface{})) func(*juggler.Conn, juggler.ConnState) {
	return func(c *juggler.Conn, state juggler.ConnState) {
		switch state {
		case juggler.Connected:
			logFn("%v: connected from %v with subprotocol %q", c.UUID, c.RemoteAddr(), c.Subprotocol())
		case juggler.Closed:
			logFn("%v: closing from %v with error %v", c.UUID, c.RemoteAddr(), c.CloseErr)
		}
	}
}

// LogMsg returns a juggler.Handler that logs messages received or sent on
// the connection to the provided logger function.
func LogMsg(logFn func(string, ...interface{})) juggler.Handler {
	return juggler.HandlerFunc(func(ctx context.Context, c *juggler.Conn, m message.Msg) {
		if m.Type().IsRead() {
			logFn("%v: received message %v %s", c.UUID, m.UUID(), m.Type())
		} else if m.Type().IsWrite() {
			logFn("%v: sending message %v %s", c.UUID, m.UUID(), m.Type())
		}
	})
}
