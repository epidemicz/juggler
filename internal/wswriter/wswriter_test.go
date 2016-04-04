package wswriter

import (
	"io/ioutil"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/assert"
)

func TestLimitedWriter(t *testing.T) {
	t.Parallel()

	// use int8/uint8 to keep size reasonable
	checker := func(limit, n uint8) bool {
		// create a limited writer with the specified limit
		w := Limit(ioutil.Discard, int64(limit))
		// create the payload for each write
		p := make([]byte, n)

		var cnt, tot int
		var err error
		for {
			cnt, err = w.Write(p)
			tot += cnt
			if err != nil {
				break
			}
		}

		// property 1: the total number of bytes written cannot be > limit.
		if tot > int(limit) {
			return false
		}
		// property 2: by writing repeatedly, it necessarily terminates with
		// an errWriteLimitExceeded
		return err == ErrWriteLimitExceeded
	}
	assert.NoError(t, quick.Check(checker, nil))
}
