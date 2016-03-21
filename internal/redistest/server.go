// Package redistest provides test helpers to manage a redis server.
package redistest

import (
	"io"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/garyburd/redigo/redis"
	"github.com/stretchr/testify/require"
)

// ClusterConfig is the configuration to use for servers started in
// redis-cluster mode. The value must contain a single reference to
// an integer placeholder (%d), the port number.
var ClusterConfig = `
port %d
cluster-enabled yes
cluster-config-file nodes.%[1]d.conf
cluster-node-timeout 5000
appendonly yes
`

// StartServer starts a redis-server instance on a free port.
// It returns the started *exec.Cmd and the port used. The caller
// should make sure to stop the command. If the redis-server
// command is not found in the PATH, the test is skipped.
//
// If w is not nil, both stdout and stderr of the server are
// written to it. If a configuration is specified, it is supplied
// to the server via stdin.
func StartServer(t *testing.T, w io.Writer, conf string) (*exec.Cmd, string) {
	return startServerWithConfig(t, w, conf)
}

func startServerWithConfig(t *testing.T, w io.Writer, conf string) (*exec.Cmd, string) {
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server not found in $PATH")
	}

	port := getFreePort(t)
	c := exec.Command("redis-server", "--port", port)
	if w != nil {
		c.Stderr = w
		c.Stdout = w
	}
	if conf != "" {
		c.Stdin = strings.NewReader(conf)
	}
	require.NoError(t, c.Start(), "start redis-server")

	// wait for the server to start accepting connections
	var ok bool
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", ":"+port, time.Second)
		if err == nil {
			ok = true
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, ok, "wait for redis-server to start")

	t.Logf("redis-server started on port %s", port)
	return c, port
}

func getFreePort(t *testing.T) string {
	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err, "listen on port 0")
	defer l.Close()
	_, p, err := net.SplitHostPort(l.Addr().String())
	require.NoError(t, err, "parse host and port")
	return p
}

// NewPool creates a redis pool to return connections on the specified
// addr.
func NewPool(t *testing.T, addr string) *redis.Pool {
	return &redis.Pool{
		MaxIdle:     2,
		MaxActive:   10,
		IdleTimeout: time.Minute,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", addr)
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}
}
