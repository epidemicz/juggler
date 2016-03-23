package redistest

import (
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// MockServer is a mock redis server.
type MockServer struct {
	Addr string

	l net.Listener
}

// StartMockServer creates and starts a mock redis server.
func StartMockServer(t *testing.T, handler func(w io.Writer, cmd string, args ...interface{})) *MockServer {
	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err, "net.Listen")

	ms := &MockServer{
		Addr: l.Addr().String(),
		l:    l,
	}
	return ms
}
