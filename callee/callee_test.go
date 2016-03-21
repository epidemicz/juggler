package callee

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/PuerkitoBio/juggler"
	"github.com/PuerkitoBio/juggler/broker"
	"github.com/PuerkitoBio/juggler/message"
	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockCalleeBroker struct {
	cps []*message.CallPayload
	err error
	rps []*message.ResPayload
}

func (b *mockCalleeBroker) Result(rp *message.ResPayload, timeout time.Duration) error {
	b.rps = append(b.rps, rp)
	return nil
}

func (b *mockCalleeBroker) NewCallsConn(uris ...string) (broker.CallsConn, error) {
	return &mockCallsConn{cps: b.cps, err: b.err}, nil
}

type mockCallsConn struct {
	cps []*message.CallPayload
	err error
}

func (c *mockCallsConn) Calls() <-chan *message.CallPayload {
	ch := make(chan *message.CallPayload)
	go func() {
		for _, cp := range c.cps {
			ch <- cp
		}
		close(ch)
	}()
	return ch
}

func (c *mockCallsConn) CallsErr() error { return c.err }
func (c *mockCallsConn) Close() error    { return nil }

func okThunk(cp *message.CallPayload) (interface{}, error) {
	time.Sleep(time.Millisecond)
	return "ok", nil
}

func errThunk(cp *message.CallPayload) (interface{}, error) {
	time.Sleep(time.Millisecond)
	return nil, io.ErrUnexpectedEOF
}

func TestCallee(t *testing.T) {
	cuid := uuid.NewRandom()
	brk := &mockCalleeBroker{
		cps: []*message.CallPayload{
			{ConnUUID: cuid, MsgUUID: uuid.NewRandom(), URI: "ok", TTLAfterRead: time.Second},
			{ConnUUID: cuid, MsgUUID: uuid.NewRandom(), URI: "err", TTLAfterRead: time.Second},
			{ConnUUID: cuid, MsgUUID: uuid.NewRandom(), URI: "ok", TTLAfterRead: time.Millisecond}, // result will be dropped
			{ConnUUID: cuid, MsgUUID: uuid.NewRandom(), URI: "err", TTLAfterRead: time.Second},
		},
		err: io.EOF,
	}

	var er message.ErrResult
	er.Error.Message = io.ErrUnexpectedEOF.Error()
	b, err := json.Marshal(er)
	require.NoError(t, err, "Marshal ErrResult")

	exp := []*message.ResPayload{
		{ConnUUID: cuid, MsgUUID: brk.cps[0].MsgUUID, URI: "ok", Args: json.RawMessage(`"ok"`)},
		{ConnUUID: cuid, MsgUUID: brk.cps[1].MsgUUID, URI: "err", Args: b},
		{ConnUUID: cuid, MsgUUID: brk.cps[3].MsgUUID, URI: "err", Args: b},
	}

	cle := &Callee{Broker: brk, LogFunc: juggler.DiscardLog}
	err = cle.Listen(map[string]Thunk{
		"ok":  okThunk,
		"err": errThunk,
	})

	assert.Equal(t, io.EOF, err, "Listen returns expected error")
	assert.Equal(t, exp, brk.rps, "got expected results")
}
