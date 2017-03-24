package client

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"io/ioutil"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/context"

	"github.com/mna/juggler/internal/wstest"
	"github.com/mna/juggler/internal/wswriter"
	"github.com/mna/juggler/message"
	"github.com/gorilla/websocket"
	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientClose(t *testing.T) {
	done := make(chan bool, 1)
	srv := wstest.StartRecordingServer(t, done, ioutil.Discard)
	defer srv.Close()

	h := HandlerFunc(func(ctx context.Context, m message.Msg) {})
	cli, err := Dial(&websocket.Dialer{}, srv.URL, nil, SetHandler(h))
	require.NoError(t, err, "Dial")

	_, err = cli.Call("a", "b", 0)
	require.NoError(t, err, "Call")

	require.NoError(t, cli.Close(), "Close")
	if err := cli.Close(); assert.Error(t, err, "Close") {
		assert.Contains(t, err.Error(), "closed connection", "2nd Close")

		if _, err := cli.Call("c", "d", 0); assert.Error(t, err, "Call after Close") {
			assert.Contains(t, err.Error(), "closed connection", "2nd Close")
		}

		if err := cli.Close(); assert.Error(t, err, "3rd Close") {
			assert.Contains(t, err.Error(), "closed connection", "3rd Close")
		}
	}
}

func TestClientReadLimit(t *testing.T) {
	done := make(chan bool, 1)
	sent := make(chan int)
	srv := wstest.StartServer(t, done, func(c *websocket.Conn) {
		var m map[string]interface{}
		// read the CALL message
		require.NoError(t, c.ReadJSON(&m), "ReadJSON")

		// write too many bytes
		w, err := c.NextWriter(websocket.TextMessage)
		require.NoError(t, err, "NextWriter")
		_, err = io.Copy(w, io.LimitReader(rand.Reader, 100))
		require.NoError(t, err, "io.Copy")
		sent <- 1
	})
	defer srv.Close()

	h := HandlerFunc(func(ctx context.Context, m message.Msg) {})
	cli, err := Dial(&websocket.Dialer{}, srv.URL, nil, SetHandler(h), SetReadLimit(90))
	require.NoError(t, err, "Dial")

	// Make a call request, should succeed, but trigger an error
	_, err = cli.Call("a", "payload", time.Second)
	assert.NoError(t, err, "Call")
	<-sent
	// wait for the response to arrive :(
	time.Sleep(100 * time.Millisecond)

	// Close returns the error
	err = cli.Close()
	if assert.Error(t, err, "Close") {
		assert.NotContains(t, err.Error(), "closed connection", "not a normal closed connection error")

		// Future calls return the same error
		_, err2 := cli.Call("a", "payload", time.Second)
		assert.Equal(t, err, err2, "Call fails with same error")
	}
}

func TestClientHandler(t *testing.T) {
	done := make(chan bool, 1)
	srv := wstest.StartServer(t, done, func(c *websocket.Conn) {
		for {
			_, r, err := c.NextReader()
			if err != nil {
				return
			}
			m, err := message.UnmarshalRequest(r)
			if !assert.NoError(t, err, "UnmarshalRequest") {
				return
			}

			call := m.(*message.Call)
			switch call.Payload.URI {
			case "ok":
				// return an ack and a result
				ack := message.NewAck(call)
				if !assert.NoError(t, c.WriteJSON(ack), "WriteJSON ACK") {
					return
				}

			case "delay":
				// return an ack and a delayed result
				ack := message.NewAck(call)
				if !assert.NoError(t, c.WriteJSON(ack)) {
					return
				}
				time.Sleep(100 * time.Millisecond)

			case "ko":
				// return a nack
				nack := message.NewNack(call, 500, io.EOF)
				if !assert.NoError(t, c.WriteJSON(nack), "WriteJSON NACK") {
					return
				}
				continue

			default:
				t.Errorf("unexpected URI: %s", call.Payload.URI)
				return
			}

			res := message.NewRes(&message.ResPayload{
				MsgUUID: call.UUID(),
				URI:     call.Payload.URI,
				Args:    []byte(`"ok"`),
			})
			if !assert.NoError(t, c.WriteJSON(res), "WriteJSON RES") {
				return
			}
		}
	})
	defer srv.Close()

	mu := sync.Mutex{}
	wg := sync.WaitGroup{}
	recv := make(map[string]bool)

	// record a set of msg type + msg ForUUID
	h := HandlerFunc(func(ctx context.Context, m message.Msg) {
		defer wg.Done()

		var forUUID string
		switch m := m.(type) {
		case *message.Ack:
			forUUID = m.Payload.For.String()
		case *message.Nack:
			forUUID = m.Payload.For.String()
		case *message.Res:
			forUUID = m.Payload.For.String()
		case *Exp:
			forUUID = m.Payload.For.String()
		default:
			t.Errorf("unexpected message type: %T", m)
			return
		}

		key := m.Type().String() + forUUID
		mu.Lock()
		assert.False(t, recv[key], "handler doesn't receive duplicates")
		recv[key] = true
		mu.Unlock()
	})

	cli, err := Dial(&websocket.Dialer{}, srv.URL, nil,
		SetHandler(h), SetAcquireWriteLockTimeout(time.Second),
		SetReadTimeout(time.Second), SetWriteTimeout(time.Second),
		SetWriteLimit(512), SetCallTimeout(50*time.Millisecond))
	require.NoError(t, err, "Dial")

	wg.Add(2)
	uidok, err := cli.Call("ok", "payload", time.Second)
	require.NoError(t, err, "Call with ok")
	wg.Add(1)
	uidko, err := cli.Call("ko", "payload", time.Second)
	require.NoError(t, err, "Call with ko")
	wg.Add(2)
	uidexp, err := cli.Call("delay", "payload", 0)
	require.NoError(t, err, "Call with delay")

	data := make([]byte, 512)
	_, err = rand.Read(data)
	require.NoError(t, err, "rand.Read")
	_, limitErr := cli.Call("delay", data, time.Second)
	if assert.Error(t, limitErr, "Call too big") {
		assert.Equal(t, wswriter.ErrWriteLimitExceeded, limitErr, "expected error")
	}

	wg.Wait()
	// wait for timeout, will make the delay RES arrive, yet should not trigger a handler call
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, limitErr, cli.Close(), "Close returns the write limit error")
	<-done

	mu.Lock()
	defer mu.Unlock()
	if assert.Equal(t, 5, len(recv), "received messages has expected length") {
		assert.True(t, recv["ACK"+uidok.String()], "ACK ok")
		assert.True(t, recv["RES"+uidok.String()], "RES ok")
		assert.True(t, recv["NACK"+uidko.String()], "NACK ko")
		assert.True(t, recv["ACK"+uidexp.String()], "ACK delay")
		assert.True(t, recv["EXP"+uidexp.String()], "EXP delay")
	}
}

func TestClientSend(t *testing.T) {
	var buf bytes.Buffer
	done := make(chan bool, 1)
	srv := wstest.StartRecordingServer(t, done, &buf)
	defer srv.Close()

	// the only received message should be EXP
	var (
		mu         sync.Mutex
		cnt        int
		expForUUID uuid.UUID
		wg         sync.WaitGroup
	)
	h := HandlerFunc(func(ctx context.Context, m message.Msg) {
		defer wg.Done()

		mu.Lock()
		cnt++
		if assert.Equal(t, ExpMsg, m.Type(), "Expects EXP message") {
			expForUUID = m.(*Exp).Payload.For
		}
		mu.Unlock()
	})

	cli, err := Dial(&websocket.Dialer{}, srv.URL, nil, SetHandler(h), SetCallTimeout(time.Millisecond))
	require.NoError(t, err, "Dial")

	// call
	wg.Add(1)
	type expected struct {
		uid uuid.UUID
		mt  message.Type
	}
	var expectedResults []expected
	callUUID, err := cli.Call("a", "call", 0)
	require.NoError(t, err, "Call")
	expectedResults = append(expectedResults, expected{callUUID, message.CallMsg})

	uid, err := cli.Pub("b", "pub")
	require.NoError(t, err, "Pub")
	expectedResults = append(expectedResults, expected{uid, message.PubMsg})

	uid, err = cli.Sub("c", false)
	require.NoError(t, err, "Sub")
	expectedResults = append(expectedResults, expected{uid, message.SubMsg})

	uid, err = cli.Unsb("d", true)
	require.NoError(t, err, "Unsb")
	expectedResults = append(expectedResults, expected{uid, message.UnsbMsg})

	// wait for any pending handlers
	wg.Wait()

	cli.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, cnt, "Expected calls to Handler")
	assert.Equal(t, callUUID, expForUUID, "Expired message should be for the Call message")

	// read the messages received by the server
	var p json.RawMessage
	r := bytes.NewReader(buf.Bytes())
	dec := json.NewDecoder(r)
	for i, exp := range expectedResults {
		require.NoError(t, dec.Decode(&p), "Decode %d", i)
		m, err := message.Unmarshal(bytes.NewReader(p))
		require.NoError(t, err, "Unmarshal %d", i)
		assert.Equal(t, exp.uid, m.UUID(), "%d: uuid", i)
		assert.Equal(t, exp.mt, m.Type(), "%d: type", i)
	}

	// no superfluous bytes
	finalErr := dec.Decode(&p)
	if assert.Error(t, finalErr, "Decode after expected results") {
		assert.Equal(t, io.EOF, finalErr, "EOF")
	}
}

func TestClientConcurrent(t *testing.T) {
	done := make(chan bool, 1)
	srv := wstest.StartRecordingServer(t, done, ioutil.Discard)
	defer srv.Close()

	h := HandlerFunc(func(ctx context.Context, m message.Msg) {})
	cli, err := Dial(&websocket.Dialer{}, srv.URL, nil, SetHandler(h),
		SetCallTimeout(time.Millisecond))
	require.NoError(t, err, "Dial")

	n := 2
	wg := sync.WaitGroup{}
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()

			// don't even check errors, because client may be closed by other goro
			cli.Call("a", "call", 0)
			cli.Pub("b", "pub")
			cli.Sub("c", false)
			cli.Unsb("d", true)

			cli.Close()
		}()
	}
	wg.Wait()
	<-done
	<-cli.CloseNotify()
}
