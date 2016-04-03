// Package wswriter implements an exclusive writer for a websocket connection.
// It allows a single access to the writer end of the websocket connection
// at any given time.
package wswriter

import (
	"errors"
	"io"
	"time"

	"github.com/gorilla/websocket"
)

// ErrWriteLockTimeout is returned when a call to Write fails
// because the write lock of the connection cannot be acquired before
// the timeout.
var ErrWriteLockTimeout = errors.New("juggler: timed out waiting for write lock")

// Writer implements an io.WriteCloser that acquires the connection's write
// lock prior to writing.
type Writer struct {
	w            io.WriteCloser
	init         bool
	writeLock    chan struct{}
	lockTimeout  time.Duration
	writeTimeout time.Duration
	wsConn       *websocket.Conn
}

// New creates an exclusive websocket writer. It uses the lock channel
// to acquire and release the lock, and fails with an ErrWriteLockTimeout
// if it can't acquire one before acquireTimeout. The writeTimeout is
// used to set the write deadline on the connection, and conn is the
// websocket connection to write to.
func New(conn *websocket.Conn, lock chan struct{}, acquireTimeout time.Duration, writeTimeout time.Duration) *Writer {
	return &Writer{
		writeLock:    lock,
		lockTimeout:  acquireTimeout,
		writeTimeout: writeTimeout,
		wsConn:       conn,
	}
}

// Write writes a text message to the websocket connection. The first
// call tries to acquire the exclusive writer lock, returning
// ErrWriteLockTimeout if it fails doing so before the timeout.
func (w *Writer) Write(p []byte) (int, error) {
	if !w.init {
		var wait <-chan time.Time
		if to := w.lockTimeout; to > 0 {
			wait = time.After(to)
		}

		// try to acquire the write lock before the timeout
		select {
		case <-wait:
			return 0, ErrWriteLockTimeout

		case <-w.writeLock:
			// lock acquired, get next writer from the websocket connection
			w.init = true
			wc, err := w.wsConn.NextWriter(websocket.TextMessage)
			if err != nil {
				return 0, err
			}
			w.w = wc
			if to := w.writeTimeout; to > 0 {
				w.wsConn.SetWriteDeadline(time.Now().Add(to))
			}
		}
	}

	return w.w.Write(p)
}

// Close finishes writing the text message to the websocket connection,
// and releases the exclusive write lock.
func (w *Writer) Close() error {
	if !w.init {
		// no write, Close is a no-op
		return nil
	}

	var err error
	if w.w != nil {
		// if w.init is true, then NextWriter was called and that writer
		// must be properly closed.
		err = w.w.Close()
		w.wsConn.SetWriteDeadline(time.Time{})
	}

	// release the write lock
	w.writeLock <- struct{}{}
	return err
}
