package juggler

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mna/juggler/client"
	"github.com/mna/juggler/internal/wstest"
	"github.com/mna/juggler/internal/wswriter"
	"github.com/mna/juggler/message"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestDelegatedMethods(t *testing.T) {
	done := make(chan bool, 1)
	srv := wstest.StartRecordingServer(t, done, ioutil.Discard)
	defer srv.Close()

	wsc := wstest.Dial(t, srv.URL)
	defer wsc.Close()

	jc := newConn(wsc, &Server{})
	defer jc.Close(nil)

	addr1, addr2 := wsc.LocalAddr(), jc.LocalAddr()
	assert.Equal(t, addr1, addr2, "LocalAddr")
	addr1, addr2 = wsc.RemoteAddr(), jc.RemoteAddr()
	assert.Equal(t, addr1, addr2, "RemoteAddr")
	assert.Equal(t, wsc, jc.UnderlyingConn(), "UnderlyingConn")
}

func TestSendBinaryMessage(t *testing.T) {
	server := &Server{}
	upg := &websocket.Upgrader{Subprotocols: Subprotocols}
	srv := httptest.NewServer(Upgrade(upg, server))
	srv.URL = strings.Replace(srv.URL, "http:", "ws:", 1)
	defer srv.Close()

	cli, err := client.Dial(&websocket.Dialer{Subprotocols: Subprotocols}, srv.URL, http.Header{"Juggler-Allowed-Messages": {"pub"}})
	require.NoError(t, err, "Dial")

	wsc := cli.UnderlyingConn()
	w, err := wsc.NextWriter(websocket.BinaryMessage)
	require.NoError(t, err, "NextWriter")
	fmt.Fprint(w, "some bytes in binary form")
	require.NoError(t, w.Close(), "Close")

	select {
	case <-cli.CloseNotify():
	case <-time.After(100 * time.Millisecond):
		t.Errorf("client connection not closed as expected")
	}
}

func TestExclusiveWriter(t *testing.T) {
	var buf bytes.Buffer
	done := make(chan bool, 1)
	srv := wstest.StartRecordingServer(t, done, &buf)
	defer srv.Close()

	wsc := wstest.Dial(t, srv.URL)
	defer wsc.Close()

	jc := newConn(wsc, &Server{})
	w := jc.Writer(100 * time.Millisecond)

	_, err := fmt.Fprint(w, "a") // acquires the lock
	assert.NoError(t, err, "write a")

	wg := sync.WaitGroup{}
	wg.Add(2)

	syncReady, syncE := make(chan struct{}, 2), make(chan struct{})
	go func() { // start c-d writer
		defer wg.Done()

		w := jc.Writer(100 * time.Millisecond)
		syncReady <- struct{}{} // ready to go

		// acquire lock, will be done after write b
		_, err := fmt.Fprint(w, "c")
		assert.NoError(t, err, "write c")

		// sync with E
		syncE <- struct{}{}
		time.Sleep(20 * time.Millisecond)
		_, err = fmt.Fprint(w, "d")
		assert.NoError(t, err, "write d")

		// release lock
		require.NoError(t, w.Close(), "close cd")
	}()

	go func() { // start e writer
		defer wg.Done()

		w := jc.Writer(10 * time.Millisecond)
		syncReady <- struct{}{}

		// acquire lock should fail
		<-syncE
		_, err := fmt.Fprint(w, "e")
		if assert.Error(t, err, "write e") {
			assert.Equal(t, wswriter.ErrWriteLockTimeout, err, "write e exceeded")
		}
		require.NoError(t, w.Close(), "close e")
	}()

	<-syncReady
	<-syncReady

	_, err = fmt.Fprint(w, "b")
	assert.NoError(t, err, "write b")
	require.NoError(t, w.Close(), "close ab") // release lock

	wg.Wait()
	wsc.Close()
	<-done
	assert.Equal(t, "abcd", buf.String(), "writes are as expected")
}

func TestConnClose(t *testing.T) {
	srv := &Server{}
	conn := newConn(&websocket.Conn{}, srv)
	conn.psc, conn.resc = fakePubSubConn{}, fakeResultsConn{}

	kill := conn.CloseNotify()
	select {
	case <-kill:
		assert.Fail(t, "close channel should block until call to Close")
	default:
	}

	conn.Close(errors.New("a"))
	select {
	case <-kill:
	default:
		assert.Fail(t, "close channel should be unblocked after call to Close")
	}

	conn.Close(errors.New("b"))
	select {
	case <-kill:
	default:
		assert.Fail(t, "close channel should still be unblocked after subsequent call to Close")
	}

	assert.Equal(t, errors.New("a"), conn.CloseErr, "got expected close error")
}
