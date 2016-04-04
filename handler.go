package juggler

import (
	"encoding/json"
	"expvar"
	"fmt"
	"io"
	"runtime"
	"time"

	"golang.org/x/net/context"

	"github.com/PuerkitoBio/juggler/internal/wswriter"
	"github.com/PuerkitoBio/juggler/message"
)

// SlowProcessMsgThreshold defines the threshold at which calls to
// ProcessMsg are marked as slow in the expvar metrics, if Server.Vars
// is set. Set to 0 to disable SlowProcessMsg metrics.
var SlowProcessMsgThreshold = 50 * time.Millisecond

// Handler defines the method required for a server to handle a send or receive
// of a Msg over a connection.
type Handler interface {
	Handle(context.Context, *Conn, message.Msg)
}

// HandlerFunc is a function signature that implements the Handler
// interface.
type HandlerFunc func(context.Context, *Conn, message.Msg)

// Handle implements Handler for the HandlerFunc by calling the
// function itself.
func (h HandlerFunc) Handle(ctx context.Context, c *Conn, m message.Msg) {
	h(ctx, c, m)
}

// Chain returns a Handler that calls the provided handlers
// in order, one after the other.
func Chain(hs ...Handler) Handler {
	return HandlerFunc(func(ctx context.Context, c *Conn, m message.Msg) {
		for _, h := range hs {
			h.Handle(ctx, c, m)
		}
	})
}

// PanicRecover returns a Handler that recovers from panics that
// may happen in h and logs the panic to the server's LogFunc. The
// connection is closed on a panic.
func PanicRecover(h Handler) Handler {
	return HandlerFunc(func(ctx context.Context, c *Conn, m message.Msg) {
		defer func() {
			if e := recover(); e != nil {
				if c.srv.Vars != nil {
					c.srv.Vars.Add("RecoveredPanics", 1)
				}

				var err error
				switch e := e.(type) {
				case error:
					err = e
				default:
					err = fmt.Errorf("%v", e)
				}
				c.Close(err)

				logf(c.srv.LogFunc, "%v: recovered from panic %v; serving message %v %s", c.UUID, e, m.UUID(), m.Type())
				var b [4096]byte
				n := runtime.Stack(b[:], false)
				logf(c.srv.LogFunc, string(b[:n]))
			}
		}()
		h.Handle(ctx, c, m)
	})
}

// LogConn is a function compatible with the Server.ConnState field
// type that logs connections and disconnections to the server's LogFunc.
func LogConn(c *Conn, state ConnState) {
	switch state {
	case Connected:
		logf(c.srv.LogFunc, "%v: connected from %v with subprotocol %q", c.UUID, c.RemoteAddr(), c.Subprotocol())
	case Closing:
		logf(c.srv.LogFunc, "%v: closing from %v with error %v", c.UUID, c.RemoteAddr(), c.CloseErr)
	}
}

// LogMsg is a HandlerFunc that logs messages received or sent on
// c to the server's LogFunc.
func LogMsg(ctx context.Context, c *Conn, m message.Msg) {
	if m.Type().IsRead() {
		logf(c.srv.LogFunc, "%v: received message %v %s", c.UUID, m.UUID(), m.Type())
	} else if m.Type().IsWrite() {
		logf(c.srv.LogFunc, "%v: sending message %v %s", c.UUID, m.UUID(), m.Type())
	}
}

func saveMsgMetrics(vars *expvar.Map, m message.Msg) func() {
	vars.Add("Msgs", 1)
	if m.Type().IsRead() {
		vars.Add("MsgsRead", 1)
	}
	if m.Type().IsWrite() {
		vars.Add("MsgsWrite", 1)
	}
	if m.Type().IsStd() {
		vars.Add("Msgs"+m.Type().String(), 1)
	}

	if SlowProcessMsgThreshold > 0 {
		start := time.Now()
		return func() {
			dur := time.Now().Sub(start)
			if dur >= SlowProcessMsgThreshold {
				vars.Add("SlowProcessMsg", 1)
				if m.Type().IsStd() {
					vars.Add("SlowProcessMsg"+m.Type().String(), 1)
				}
			}
		}
	}
	return nil
}

// ProcessMsg is a HandlerFunc that implements the default message
// processing. For client messages, it calls the appropriate RPC
// or pub-sub mechanisms. For server messages, it marshals
// the message and sends it to the client.
//
// When a custom Handler is set on the Server, it should at some
// point call ProcessMsg so the expected behaviour happens.
func ProcessMsg(ctx context.Context, c *Conn, m message.Msg) {
	addFn := func(string, int64) {}
	if c.srv.Vars != nil {
		if fn := saveMsgMetrics(c.srv.Vars, m); fn != nil {
			defer fn()
		}

		addFn = c.srv.Vars.Add
	}

	switch m := m.(type) {
	case *message.Call:
		cp := &message.CallPayload{
			ConnUUID: c.UUID,
			MsgUUID:  m.UUID(),
			URI:      m.Payload.URI,
			Args:     m.Payload.Args,
		}
		if err := c.srv.CallerBroker.Call(cp, m.Payload.Timeout); err != nil {
			c.Send(message.NewNack(m, 500, err))
			return
		}
		c.Send(message.NewAck(m))

	case *message.Pub:
		pp := &message.PubPayload{
			MsgUUID: m.UUID(),
			Args:    m.Payload.Args,
		}
		if err := c.srv.PubSubBroker.Publish(m.Payload.Channel, pp); err != nil {
			c.Send(message.NewNack(m, 500, err))
			return
		}
		c.Send(message.NewAck(m))

	case *message.Sub:
		if err := c.psc.Subscribe(m.Payload.Channel, m.Payload.Pattern); err != nil {
			c.Send(message.NewNack(m, 500, err))
			return
		}
		c.Send(message.NewAck(m))

	case *message.Unsb:
		if err := c.psc.Unsubscribe(m.Payload.Channel, m.Payload.Pattern); err != nil {
			c.Send(message.NewNack(m, 500, err))
			return
		}
		c.Send(message.NewAck(m))

	case *message.Ack:
		doWrite(c, m, addFn)
	case *message.Nack:
		doWrite(c, m, addFn)
	case *message.Evnt:
		doWrite(c, m, addFn)
	case *message.Res:
		doWrite(c, m, addFn)

	default:
		addFn("MsgsUnknown", 1)
		logf(c.srv.LogFunc, "unknown message in ProcessMsg: %T", m)
	}
}

func doWrite(c *Conn, m message.Msg, addFn func(string, int64)) {
	if err := writeMsg(c, m); err != nil {
		switch err {
		case ErrWriteLockTimeout:
			addFn("WriteLockTimeouts", 1)
			c.Close(fmt.Errorf("writeMsg failed: %v; closing connection", err))

		case wswriter.ErrWriteLimitExceeded:
			addFn("WriteLimitExceeded", 1)
			logf(c.srv.LogFunc, "%v: writeMsg %v failed: %v", c.UUID, m.UUID(), err)

			// no good http code for this case
			if err := writeMsg(c, message.NewNack(m, 599, err)); err != nil {
				if err == ErrWriteLockTimeout {
					addFn("WriteLockTimeouts", 1)
					c.Close(fmt.Errorf("writeMsg failed: %v; closing connection", err))
				} else {
					logf(c.srv.LogFunc, "%v: writeMsg %v for write limit exceeded notification failed: %v", c.UUID, m.UUID(), err)
				}
				return
			}

		default:
			logf(c.srv.LogFunc, "%v: writeMsg %v failed: %v", c.UUID, m.UUID(), err)
		}
	}
}

func writeMsg(c *Conn, m message.Msg) error {
	w := c.Writer(c.srv.AcquireWriteLockTimeout)
	defer w.Close()

	lw := io.Writer(w)
	if l := c.srv.WriteLimit; l > 0 {
		lw = wswriter.Limit(w, l)
	}
	if err := json.NewEncoder(lw).Encode(m); err != nil {
		return err
	}
	return nil
}
