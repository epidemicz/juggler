package message

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalUnmarshal(t *testing.T) {
	t.Parallel()

	call, err := NewCall("a", map[string]interface{}{"x": 3}, time.Second)
	require.NoError(t, err, "NewCall")
	pub, err := NewPub("d", map[string]interface{}{"y": "ok"})
	require.NoError(t, err, "NewPub")
	rp := &ResPayload{
		ConnUUID: uuid.NewRandom(),
		MsgUUID:  uuid.NewRandom(),
		URI:      "g",
		Args:     json.RawMessage("null"),
	}
	ep := &EvntPayload{
		MsgUUID: uuid.NewRandom(),
		Channel: "h",
		Pattern: "h*",
		Args:    json.RawMessage(`"string"`),
	}

	cases := []Msg{
		call,
		NewSub("b", false),
		NewUnsb("c", true),
		pub,
		NewNack(call, 500, io.EOF),
		NewAck(pub),
		NewRes(rp),
		NewEvnt(ep),
	}
	for i, m := range cases {
		b, err := json.Marshal(m)
		require.NoError(t, err, "Marshal %d", i)

		mm, err := Unmarshal(bytes.NewReader(b))
		require.NoError(t, err, "Unmarshal %d", i)

		// for NackMsg, the Nack is not marshaled, so zero it before the comparison
		if m.Type() == NackMsg {
			m.(*Nack).Payload.Err = nil
		}

		assert.True(t, reflect.DeepEqual(m, mm), "DeepEqual %d", i)

		_, err = UnmarshalRequest(bytes.NewReader(b))
		assert.Equal(t, m.Type().IsRead(), err == nil, "UnmarshalRequest for %d", i)

		_, err = UnmarshalResponse(bytes.NewReader(b))
		assert.Equal(t, m.Type().IsWrite(), err == nil, "UnmarshalResponse for %d", i)
	}
}

func TestNewNackFromAck(t *testing.T) {
	t.Parallel()

	pub, err := NewPub("d", map[string]interface{}{"y": "ok"})
	require.NoError(t, err, "NewPub")
	ack := NewAck(pub)
	nack := NewNack(ack, 500, io.EOF)

	// should keep the "from" information of Ack
	assert.Equal(t, nack.Payload.For, ack.Payload.For, "For")
	assert.Equal(t, nack.Payload.ForType, ack.Payload.ForType, "ForType")
	assert.Equal(t, nack.Payload.URI, ack.Payload.URI, "URI")
	assert.Equal(t, nack.Payload.Channel, ack.Payload.Channel, "Channel")
}

func TestRegister(t *testing.T) {
	nm := uuid.NewRandom().String() // avoid failures when running tests multiple times

	typ := Register(nm)
	assert.False(t, typ.IsRead(), "IsRead is false")
	assert.False(t, typ.IsWrite(), "IsWrite is false")
	assert.False(t, typ.IsStd(), "IsStd is false")
	assert.Equal(t, nm, typ.String(), "String")

	assert.Panics(t, func() {
		Register(nm)
	}, "Registering twice panics")
}

func TestUnknownType(t *testing.T) {
	unkTyp := Type(nextCustomMsg)
	assert.Equal(t, fmt.Sprintf("<unknown: %d>", unkTyp), unkTyp.String())
}

func TestUnmarshalIfUnknown(t *testing.T) {
	meta := NewMeta(Type(-1)) // invalid message
	b, err := json.Marshal(partialMsg{Meta: meta})
	require.NoError(t, err, "Marshal failed")
	_, err = unmarshalIf(bytes.NewReader(b), Type(-1))
	assert.Error(t, err)
	t.Log(err)
}

func TestUnmarshalRequest(t *testing.T) {
	call, err := NewCall("u", "payload", time.Second)
	require.NoError(t, err, "NewCall failed")
	sub := NewSub("c", false)
	unsb := NewUnsb("d", false)
	pub, err := NewPub("p", "payload")
	require.NoError(t, err, "NewPub failed")
	ack := NewAck(pub)

	cases := []struct {
		v       interface{}
		allowed []Type
		wantErr bool
	}{
		{call, nil, false},
		{sub, nil, false},
		{unsb, nil, false},
		{pub, nil, false},
		{ack, nil, true},
		{call, []Type{CallMsg}, false},
		{sub, []Type{SubMsg}, false},
		{unsb, []Type{UnsbMsg}, false},
		{pub, []Type{PubMsg}, false},
		{ack, []Type{AckMsg}, true}, // Ack not a request message type, still not allowed
		{call, []Type{CallMsg, PubMsg}, false},
		{sub, []Type{CallMsg, PubMsg}, true},
		{unsb, []Type{CallMsg, PubMsg}, true},
		{pub, []Type{CallMsg, PubMsg}, false},
		{ack, []Type{CallMsg, PubMsg}, true},
	}
	for i, c := range cases {
		b, err := json.Marshal(c.v)
		require.NoError(t, err, "%d: Marshal failed", i)
		_, err = UnmarshalRequest(bytes.NewReader(b), c.allowed...)

		if !assert.Equal(t, err != nil, c.wantErr, "%d", i) {
			t.Logf("%d: want error? %t, got %v", i, c.wantErr, err)
		}
	}
}
