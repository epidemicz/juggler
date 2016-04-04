// Package wswriter implements an exclusive writer and a limited writer
// for websocket connections.
package wswriter

import (
	"errors"
	"io"
	"time"

	"github.com/gorilla/websocket"
)

// ErrWriteLockTimeout is returned when a Write call to an exclusive writer
// fails because the write lock of the connection cannot be acquired before
// the timeout.
var ErrWriteLockTimeout = errors.New("juggler: timed out waiting for write lock")

// exclusiveWriter implements an io.WriteCloser that acquires the
// connection's write lock prior to writing.
type exclusiveWriter struct {
	w            io.WriteCloser
	init         bool
	writeLock    chan struct{}
	lockTimeout  time.Duration
	writeTimeout time.Duration
	wsConn       *websocket.Conn
}

// Exclusive creates an exclusive websocket writer. It uses the lock channel
// to acquire and release the lock, and fails with an ErrWriteLockTimeout
// if it can't acquire one before acquireTimeout. The writeTimeout is
// used to set the write deadline on the connection, and conn is the
// websocket connection to write to.
func Exclusive(conn *websocket.Conn, lock chan struct{}, acquireTimeout, writeTimeout time.Duration) io.WriteCloser {
	return &exclusiveWriter{
		writeLock:    lock,
		lockTimeout:  acquireTimeout,
		writeTimeout: writeTimeout,
		wsConn:       conn,
	}
}

// Write writes a text message to the websocket connection. The first
// call tries to acquire the exclusive writer lock, returning
// ErrWriteLockTimeout if it fails doing so before the timeout.
func (w *exclusiveWriter) Write(p []byte) (int, error) {
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
func (w *exclusiveWriter) Close() error {
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

// ErrWriteLimitExceeded is returned when a Write call to a limited
// writer fails because the limit is exceeded.
var ErrWriteLimitExceeded = errors.New("write limit exceeded")

type limitedWriter struct {
	w io.Writer
	n int64
}

// Limit returns an io.Writer that can write up to limit bytes to w.
// Exceeding the limit fails with ErrWriteLimitExceeded.
func Limit(w io.Writer, limit int64) io.Writer {
	return &limitedWriter{w: w, n: limit}
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.n -= int64(len(p))
	if w.n < 0 {
		return 0, ErrWriteLimitExceeded
	}
	return w.w.Write(p)
}
