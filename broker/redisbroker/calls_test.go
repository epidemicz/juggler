package redisbroker

import (
	"sync"
	"testing"
	"time"

	"github.com/mna/juggler/message"
	"github.com/mna/redisc/redistest"
	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalls(t *testing.T) {
	cmd, port := redistest.StartServer(t, nil, "")
	defer cmd.Process.Kill()

	pool := redistest.NewPool(t, ":"+port)
	brk := &Broker{
		Pool:            pool,
		Dial:            pool.Dial,
		BlockingTimeout: time.Second,
		LogFunc:         logIfVerbose,
	}

	// list calls on URI "a"
	cc, err := brk.NewCallsConn("a")
	require.NoError(t, err, "get Calls connection")

	// keep track of received calls
	wg := sync.WaitGroup{}
	wg.Add(1)
	var uuids []uuid.UUID
	go func() {
		defer wg.Done()
		for cp := range cc.Calls() {
			uuids = append(uuids, cp.MsgUUID)
		}
	}()

	// wait 1s to test the ErrNil case
	time.Sleep(1100 * time.Millisecond)

	cases := []struct {
		cp      *message.CallPayload
		timeout time.Duration
		exp     bool
	}{
		{&message.CallPayload{ConnUUID: uuid.NewRandom(), MsgUUID: uuid.NewRandom(), URI: "a"}, time.Second, true},
		{&message.CallPayload{ConnUUID: uuid.NewRandom(), MsgUUID: uuid.NewRandom(), URI: "b"}, time.Second, false},
		{&message.CallPayload{ConnUUID: uuid.NewRandom(), MsgUUID: uuid.NewRandom(), URI: "a"}, time.Minute, true},
	}
	var expected []uuid.UUID
	for i, c := range cases {
		if c.exp {
			expected = append(expected, c.cp.MsgUUID)
		}
		require.NoError(t, brk.Call(c.cp, c.timeout), "Call %d", i)
	}

	time.Sleep(10 * time.Millisecond) // ensure time to pop the last message :(
	require.NoError(t, cc.Close(), "close calls connection")
	wg.Wait()
	if assert.Error(t, cc.CallsErr(), "CallsErr returns the error") {
		assert.Contains(t, cc.CallsErr().Error(), "use of closed network connection", "CallsErr is the expected error")
	}
	assert.Equal(t, expected, uuids, "got expected UUIDs")
}
