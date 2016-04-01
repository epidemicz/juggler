package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/juggler/message"
	"github.com/garyburd/redigo/redis"
	"github.com/pborman/uuid"
)

var (
	durationFlag  = flag.Duration("d", 10*time.Second, "Duration of the test.")
	execTypeFlag  = flag.Int("e", 0, "Type of execution.")
	redisAddrFlag = flag.String("redis", ":6379", "Redis `address`.")
)

var callOrResScript = redis.NewScript(2, `
	redis.call("SET", KEYS[1], ARGV[1], "PX", tonumber(ARGV[1]))
	local res = redis.call("LPUSH", KEYS[2], ARGV[2])
	local limit = tonumber(ARGV[3])
	if res > limit and limit > 0 then
		local diff = res - limit
		redis.call("LTRIM", KEYS[2], diff, limit + diff)
		return redis.error_reply("list capacity exceeded")
	end
	return res
`)

var delAndPTTLScript = redis.NewScript(1, `
	local res = redis.call("PTTL", KEYS[1])
	redis.call("DEL", KEYS[1])
	return res
`)

func main() {
	flag.Parse()

	c1, err := redis.Dial("tcp", *redisAddrFlag)
	if err != nil {
		log.Fatalf("Dial failed: %v", err)
	}
	c2, err := redis.Dial("tcp", *redisAddrFlag)
	if err != nil {
		log.Fatalf("Dial failed: %v", err)
	}

	var push, pop int64
	switch *execTypeFlag {
	case 0:
		// pure push/pop with static payload
		push, pop = purePushPop(c1, c2)
	case 1:
		// and marshal a CallPayload on push
	case 2:
		// and unmarshal the CallPayload on pop
		push, pop = withMarshalUnmarshal(c1, c2)
	case 3:
		// with callOrResScript on push
	case 4:
		// and with delAndPTTLScript on pop
		push, pop = withMarshalUnmarshalPushPopScript(c1, c2)
	case 5:
		// pure push/pop with N goroutines each
		push, pop = purePushPopGoros()
	default:
		panic("unknown exec type")
	}

	fmt.Printf("push: %d, pop: %d\n", push, atomic.LoadInt64(&pop))
}

func purePushPop(c1, c2 redis.Conn) (int64, int64) {
	var push, pop int64
	go func() {
		for {
			if _, err := c2.Do("BRPOP", "test:list", 0); err != nil {
				log.Fatalf("BRPOP failed: %v", err)
			}
			atomic.AddInt64(&pop, 1)
		}
	}()

	done := time.After(*durationFlag)
loop:
	for {
		select {
		case <-done:
			break loop
		default:
		}
		if _, err := c1.Do("LPUSH", "test:list", "payload"); err != nil {
			log.Fatalf("LPUSH failed: %v", err)
		}
		push++
	}

	time.Sleep(time.Second)
	return push, atomic.LoadInt64(&pop)
}

func purePushPopGoros() (int64, int64) {
	var push, pop int64
	n := 8
	for i := 0; i < n; i++ {
		go func() {
			c, err := redis.Dial("tcp", *redisAddrFlag)
			if err != nil {
				log.Fatalf("Dial failed: %v", err)
			}
			defer c.Close()
			for {
				if _, err := c.Do("BRPOP", "test:list", 0); err != nil {
					log.Fatalf("BRPOP failed: %v", err)
				}
				atomic.AddInt64(&pop, 1)
			}
		}()
	}

	for i := 0; i < n; i++ {
		go func() {
			c, err := redis.Dial("tcp", *redisAddrFlag)
			if err != nil {
				log.Fatalf("Dial failed: %v", err)
			}
			defer c.Close()

			done := time.After(*durationFlag)
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := c.Do("LPUSH", "test:list", "payload"); err != nil {
					log.Fatalf("LPUSH failed: %v", err)
				}
				atomic.AddInt64(&push, 1)
			}
		}()
	}

	<-time.After(*durationFlag)
	time.Sleep(time.Second)
	return atomic.LoadInt64(&push), atomic.LoadInt64(&pop)
}

func withMarshalUnmarshal(c1, c2 redis.Conn) (int64, int64) {
	var push, pop int64
	go func() {
		for {
			v, err := redis.Values(c2.Do("BRPOP", "test:list", 0))
			if err != nil {
				log.Fatalf("BRPOP failed: %v", err)
			}
			var cp message.CallPayload
			if err := unmarshalBRPOPValue(&cp, v); err != nil {
				log.Fatalf("unmarshal BRPOP failed: %v", err)
			}
			atomic.AddInt64(&pop, 1)
		}
	}()

	done := time.After(*durationFlag)
loop:
	for {
		select {
		case <-done:
			break loop
		default:
		}
		cp := &message.CallPayload{
			ConnUUID: uuid.NewRandom(),
			MsgUUID:  uuid.NewRandom(),
			URI:      "test.delay",
			Args:     []byte("0"),
		}
		b, err := json.Marshal(cp)
		if err != nil {
			log.Fatalf("json.Marshal failed: %v", err)
		}
		if _, err := c1.Do("LPUSH", "test:list", b); err != nil {
			log.Fatalf("LPUSH failed: %v", err)
		}
		push++
	}

	time.Sleep(time.Second)
	return push, atomic.LoadInt64(&pop)
}

func withMarshalUnmarshalPushPopScript(c1, c2 redis.Conn) (int64, int64) {
	var push, pop int64
	go func() {
		for {
			v, err := redis.Values(c2.Do("BRPOP", "test:list", 0))
			if err != nil {
				log.Fatalf("BRPOP failed: %v", err)
			}
			var cp message.CallPayload
			if err := unmarshalBRPOPValue(&cp, v); err != nil {
				log.Fatalf("unmarshal BRPOP failed: %v", err)
			}
			if _, err := delAndPTTLScript.Do(c2, "test:expire:"+cp.MsgUUID.String()); err != nil {
				log.Fatalf("delAndPTTLScript failed: %v", err)
			}
			atomic.AddInt64(&pop, 1)
		}
	}()

	done := time.After(*durationFlag)
loop:
	for {
		select {
		case <-done:
			break loop
		default:
		}
		cp := &message.CallPayload{
			ConnUUID: uuid.NewRandom(),
			MsgUUID:  uuid.NewRandom(),
			URI:      "test.delay",
			Args:     []byte("0"),
		}
		b, err := json.Marshal(cp)
		if err != nil {
			log.Fatalf("json.Marshal failed: %v", err)
		}
		_, err = callOrResScript.Do(c1,
			"test:expire:"+cp.MsgUUID.String(), // key[1] : the SET key with expiration
			"test:list",                        // key[2] : the LIST key
			2000,                               // argv[1] : the timeout in milliseconds
			b,                                  // argv[2] : the call payload
			0,                                  // argv[3] : the LIST capacity
		)
		if err != nil {
			log.Fatalf("LPUSH failed: %v", err)
		}
		push++
	}

	time.Sleep(time.Second)
	return push, atomic.LoadInt64(&pop)
}

func unmarshalBRPOPValue(dst interface{}, src []interface{}) error {
	var p []byte
	if _, err := redis.Scan(src, nil, &p); err != nil {
		return err
	}
	if err := json.Unmarshal(p, dst); err != nil {
		return err
	}
	return nil
}
