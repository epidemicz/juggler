package juggler

import (
	"encoding/json"
	"expvar"
	"io"
	"time"

	"golang.org/x/net/context"

	"github.com/mna/juggler/internal/wswriter"
	"github.com/mna/juggler/message"
)

// SlowProcessMsgThreshold defines the threshold at which calls to
// ProcessMsg are marked as slow in the expvar metrics, if Server.Vars
// is set. Set to 0 to disable SlowProcessMsg metrics.
var SlowProcessMsgThreshold = 100 * time.Millisecond

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

// ProcessMsg implements the standard message processing. For requests
// (client-sent messages), it calls the appropriate RPC or pub-sub
// mechanisms. For responses (server-sent messages), it marshals the
// message and sends it to the client. If a write to the connection fails,
// the connection is closed and the write error is stored as CloseErr
// on the connection (unless an earlier error already caused the
// connection to close).
//
// When a custom Handler is set on the Server, it should at some
// point call ProcessMsg so the expected behaviour happens.
func ProcessMsg(c *Conn, m message.Msg) {
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

	case *message.Ack, *message.Nack, *message.Evnt, *message.Res:
		doWrite(c, m, addFn)

	default:
		addFn("MsgsUnknown", 1)
	}
}

func doWrite(c *Conn, m message.Msg, addFn func(string, int64)) {
	if err := writeMsg(c, m); err != nil {
		switch err {
		case wswriter.ErrWriteLockTimeout:
			addFn("WriteLockTimeouts", 1)
			c.Close(err)

		case wswriter.ErrWriteLimitExceeded:
			addFn("WriteLimitExceeded", 1)
			c.Close(err)

		default:
			// client may be gone
			c.Close(err)
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
	return json.NewEncoder(lw).Encode(m)
}
