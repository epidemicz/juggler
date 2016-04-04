package srvhandler

import (
	"testing"

	"github.com/PuerkitoBio/juggler"
	"github.com/PuerkitoBio/juggler/message"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
)

func TestChain(t *testing.T) {
	t.Parallel()

	var b []byte

	genHandler := func(char byte) juggler.HandlerFunc {
		return juggler.HandlerFunc(func(ctx context.Context, c *juggler.Conn, m message.Msg) {
			b = append(b, char)
		})
	}
	ch := Chain(genHandler('a'), genHandler('b'), genHandler('c'))
	ch.Handle(context.Background(), &juggler.Conn{}, &message.Ack{})

	assert.Equal(t, "abc", string(b))
}
