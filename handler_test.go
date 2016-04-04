package juggler

import (
	"errors"
	"testing"

	"github.com/PuerkitoBio/juggler/internal/jugglertest"
	"github.com/PuerkitoBio/juggler/message"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"golang.org/x/net/context"
)

func TestChain(t *testing.T) {
	t.Parallel()

	var b []byte

	genHandler := func(char byte) HandlerFunc {
		return HandlerFunc(func(ctx context.Context, c *Conn, m message.Msg) {
			b = append(b, char)
		})
	}
	ch := Chain(genHandler('a'), genHandler('b'), genHandler('c'))
	ch.Handle(context.Background(), &Conn{}, &message.Ack{})

	assert.Equal(t, "abc", string(b))
}

type fakePubSubConn struct{}

func (f fakePubSubConn) Subscribe(channel string, pattern bool) error   { return nil }
func (f fakePubSubConn) Unsubscribe(channel string, pattern bool) error { return nil }
func (f fakePubSubConn) Events() <-chan *message.EvntPayload            { return nil }
func (f fakePubSubConn) EventsErr() error                               { return nil }
func (f fakePubSubConn) Close() error                                   { return nil }

type fakeResultsConn struct{}

func (f fakeResultsConn) Results() <-chan *message.ResPayload { return nil }
func (f fakeResultsConn) ResultsErr() error                   { return nil }
func (f fakeResultsConn) Close() error                        { return nil }

func TestPanicRecover(t *testing.T) {
	t.Parallel()

	defer func() {
		require.Nil(t, recover(), "panic escaped the PanicRecover handler")
	}()

	panicer := HandlerFunc(func(ctx context.Context, c *Conn, m message.Msg) {
		panic("a")
	})
	ph := PanicRecover(panicer)

	dbgl := &jugglertest.DebugLog{T: t}
	srv := &Server{LogFunc: dbgl.Printf}
	conn := newConn(&websocket.Conn{}, srv)
	conn.psc, conn.resc = fakePubSubConn{}, fakeResultsConn{}
	ph.Handle(context.Background(), conn, &message.Ack{})

	err := conn.CloseErr
	if assert.NotNil(t, err, "connection has been closed") {
		assert.Equal(t, errors.New("a"), err, "error is as expected")
	}
	// with the stack, PanicRecover calls the log twice
	assert.Equal(t, 2, dbgl.Calls(), "log calls")
}
