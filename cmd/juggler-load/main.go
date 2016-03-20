// Command juggler-load is a juggler load generator. It runs a
// number of client connections to a server, and for a
// given duration, makes calls and collects results and statistics.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"golang.org/x/net/context"

	"github.com/PuerkitoBio/juggler"
	"github.com/PuerkitoBio/juggler/client"
	"github.com/PuerkitoBio/juggler/message"
	"github.com/gorilla/websocket"
)

var (
	addrFlag        = flag.String("addr", "ws://localhost:9000/ws", "Server `address`.")
	connFlag        = flag.Int("c", 100, "Number of `connections`.")
	durationFlag    = flag.Duration("d", 10*time.Second, "Run `duration`.")
	delayFlag       = flag.Duration("delay", 0, "Start execution after `delay`.")
	helpFlag        = flag.Bool("help", false, "Show help.")
	numURIsFlag     = flag.Int("n", 0, "Spread calls to this `number` of URIs (added as a suffix to the URI).")
	payloadFlag     = flag.String("p", "100", "Call `payload`.")
	subprotoFlag    = flag.String("proto", "juggler.0", "Websocket `subprotocol`.")
	callRateFlag    = flag.Duration("r", 100*time.Millisecond, "Call `rate` per connection.")
	callTimeoutFlag = flag.Duration("t", time.Second, "Call `timeout`.")
	uriFlag         = flag.String("u", "test.delay", "Call `URI`.")
	waitFlag        = flag.Duration("w", 5*time.Second, "Wait `duration` for connections to stop.")
)

var (
	fnMap = template.FuncMap{
		"subi": subiFn,
		"subd": subdFn,
		"subf": subfFn,
		"avg":  avgFn,
		"pctl": pctlFn,
	}

	tpl = template.Must(template.New("output").Funcs(fnMap).Parse(`
--- CONFIGURATION

Address:    {{ .Run.Addr }}
Protocol:   {{ .Run.Protocol }}
URI:        {{ .Run.URI }} x {{.Run.NURIs}}
Payload:    {{ .Run.Payload }}

Connections: {{ .Run.Conns }}
Rate:        {{ .Run.Rate | printf "%s" }}
Timeout:     {{ .Run.Timeout | printf "%s" }}
Duration:    {{ .Run.Duration | printf "%s" }}

--- CLIENT STATISTICS

Actual Duration: {{ .Run.ActualDuration | printf "%s" }}
Calls:           {{ .Run.Calls }}
Acks:            {{ .Run.Ack }}
Nacks:           {{ .Run.Nack }}
Results:         {{ .Run.Res }}
Expired:         {{ .Run.Exp }}

--- CLIENT LATENCIES

Minimum:         {{ pctl 0 .Latencies }}
Maximum:         {{ pctl 100 .Latencies }}
Average:         {{ avg .Latencies }}
Median:          {{ pctl 50 .Latencies }}
75th Percentile: {{ pctl 75 .Latencies }}
90th Percentile: {{ pctl 90 .Latencies }}
99th Percentile: {{ pctl 99 .Latencies }}

--- SERVER STATISTICS

Memory          Before          After           Diff.
---------------------------------------------------------------
Alloc:          {{.Before.Memstats.Alloc | printf "%-15v"}} {{.After.Memstats.Alloc | printf "%-15v"}} {{subf .After.Memstats.Alloc .Before.Memstats.Alloc | printf "%v" }}
TotalAlloc:     {{.Before.Memstats.TotalAlloc | printf "%-15v"}} {{.After.Memstats.TotalAlloc | printf "%-15v"}} {{subf .After.Memstats.TotalAlloc .Before.Memstats.TotalAlloc | printf "%v" }}
Mallocs:        {{.Before.Memstats.Mallocs | printf "%-15d"}} {{.After.Memstats.Mallocs | printf "%-15d"}} {{subi .After.Memstats.Mallocs .Before.Memstats.Mallocs }}
Frees:          {{.Before.Memstats.Frees | printf "%-15d"}} {{.After.Memstats.Frees | printf "%-15d"}} {{subi .After.Memstats.Frees .Before.Memstats.Frees }}
HeapAlloc:      {{.Before.Memstats.HeapAlloc | printf "%-15v"}} {{.After.Memstats.HeapAlloc | printf "%-15v"}} {{subf .After.Memstats.HeapAlloc .Before.Memstats.HeapAlloc | printf "%v" }}
HeapInuse:      {{.Before.Memstats.HeapInuse | printf "%-15v"}} {{.After.Memstats.HeapInuse | printf "%-15v"}} {{subf .After.Memstats.HeapInuse .Before.Memstats.HeapInuse | printf "%v" }}
HeapObjects:    {{.Before.Memstats.HeapObjects | printf "%-15d"}} {{.After.Memstats.HeapObjects | printf "%-15d"}} {{subi .After.Memstats.HeapObjects .Before.Memstats.HeapObjects }}
StackInuse:     {{.Before.Memstats.StackInuse | printf "%-15v"}} {{.After.Memstats.StackInuse | printf "%-15v"}} {{subf .After.Memstats.StackInuse .Before.Memstats.StackInuse | printf "%v" }}
NumGC:          {{.Before.Memstats.NumGC | printf "%-15d"}} {{.After.Memstats.NumGC | printf "%-15d"}} {{subi .After.Memstats.NumGC .Before.Memstats.NumGC }}
PauseTotalNs:   {{.Before.Memstats.PauseTotalNs | printf "%-15v"}} {{.After.Memstats.PauseTotalNs | printf "%-15v"}} {{subd .After.Memstats.PauseTotalNs .Before.Memstats.PauseTotalNs | printf "%v" }}

Counter             Before          After           Diff.
----------------------------------------------------------------
ActiveConnGoros:    {{.Before.Juggler.ActiveConnGoros | printf "%-15d"}} {{.After.Juggler.ActiveConnGoros | printf "%-15d"}} {{subi .After.Juggler.ActiveConnGoros .Before.Juggler.ActiveConnGoros }}
ActiveConns:        {{.Before.Juggler.ActiveConns | printf "%-15d"}} {{.After.Juggler.ActiveConns | printf "%-15d"}} {{subi .After.Juggler.ActiveConns .Before.Juggler.ActiveConns }}
MsgsRead:           {{.Before.Juggler.MsgsRead | printf "%-15d"}} {{.After.Juggler.MsgsRead | printf "%-15d"}} {{subi .After.Juggler.MsgsRead .Before.Juggler.MsgsRead }}
MsgsWrite:          {{.Before.Juggler.MsgsWrite | printf "%-15d"}} {{.After.Juggler.MsgsWrite | printf "%-15d"}} {{subi .After.Juggler.MsgsWrite .Before.Juggler.MsgsWrite }}
MsgsCALL:           {{.Before.Juggler.MsgsCALL | printf "%-15d"}} {{.After.Juggler.MsgsCALL | printf "%-15d"}} {{subi .After.Juggler.MsgsCALL .Before.Juggler.MsgsCALL }}
MsgsACK:             {{.Before.Juggler.MsgsACK | printf "%-15d"}} {{.After.Juggler.MsgsACK | printf "%-15d"}} {{subi .After.Juggler.MsgsACK .Before.Juggler.MsgsACK }}
MsgsNACK:            {{.Before.Juggler.MsgsNACK | printf "%-15d"}} {{.After.Juggler.MsgsNACK | printf "%-15d"}} {{subi .After.Juggler.MsgsNACK .Before.Juggler.MsgsNACK }}
Msgs:               {{.Before.Juggler.Msgs | printf "%-15d"}} {{.After.Juggler.Msgs | printf "%-15d"}} {{subi .After.Juggler.Msgs .Before.Juggler.Msgs }}
MsgsRES:            {{.Before.Juggler.MsgsRES | printf "%-15d"}} {{.After.Juggler.MsgsRES | printf "%-15d"}} {{subi .After.Juggler.MsgsRES .Before.Juggler.MsgsRES }}
RecoveredPanics:    {{.Before.Juggler.RecoveredPanics | printf "%-15d"}} {{.After.Juggler.RecoveredPanics | printf "%-15d"}} {{subi .After.Juggler.RecoveredPanics .Before.Juggler.RecoveredPanics }}
SlowProcessMsg:     {{.Before.Juggler.SlowProcessMsg | printf "%-15d"}} {{.After.Juggler.SlowProcessMsg | printf "%-15d"}} {{subi .After.Juggler.SlowProcessMsg .Before.Juggler.SlowProcessMsg }}
SlowProcessMsgCALL: {{.Before.Juggler.SlowProcessMsgCALL | printf "%-15d"}} {{.After.Juggler.SlowProcessMsgCALL | printf "%-15d"}} {{subi .After.Juggler.SlowProcessMsgCALL .Before.Juggler.SlowProcessMsgCALL }}
SlowProcessMsgACK:   {{.Before.Juggler.SlowProcessMsgACK | printf "%-15d"}} {{.After.Juggler.SlowProcessMsgACK | printf "%-15d"}} {{subi .After.Juggler.SlowProcessMsgACK .Before.Juggler.SlowProcessMsgACK }}
SlowProcessMsgNACK:  {{.Before.Juggler.SlowProcessMsgNACK | printf "%-15d"}} {{.After.Juggler.SlowProcessMsgNACK | printf "%-15d"}} {{subi .After.Juggler.SlowProcessMsgNACK .Before.Juggler.SlowProcessMsgNACK }}
SlowProcessMsgRES:  {{.Before.Juggler.SlowProcessMsgRES | printf "%-15d"}} {{.After.Juggler.SlowProcessMsgRES | printf "%-15d"}} {{subi .After.Juggler.SlowProcessMsgRES .Before.Juggler.SlowProcessMsgRES }}
TotalConnGoros:     {{.Before.Juggler.TotalConnGoros | printf "%-15d"}} {{.After.Juggler.TotalConnGoros | printf "%-15d"}} {{subi .After.Juggler.TotalConnGoros .Before.Juggler.TotalConnGoros }}
TotalConns:         {{.Before.Juggler.TotalConns | printf "%-15d"}} {{.After.Juggler.TotalConns | printf "%-15d"}} {{subi .After.Juggler.TotalConns .Before.Juggler.TotalConns }}

`))
)

func subiFn(a, b int) int {
	return a - b
}

func subdFn(a, b time.Duration) time.Duration {
	return a - b
}

func subfFn(a, b byteSize) byteSize {
	return a - b
}

func avgFn(durs []time.Duration) time.Duration {
	var sum time.Duration

	if len(durs) == 0 {
		return 0
	}

	for _, d := range durs {
		sum += d
	}
	return sum / time.Duration(len(durs))
}

type durations []time.Duration

func (d durations) Len() int           { return len(d) }
func (d durations) Swap(x, y int)      { d[x], d[y] = d[y], d[x] }
func (d durations) Less(x, y int) bool { return d[x] < d[y] }

// from https://github.com/golang/go/issues/4594#issuecomment-135336012
func round(f float64) int {
	if math.Abs(f) < 0.5 {
		return 0
	}
	return int(f + math.Copysign(0.5, f))
}

func pctlFn(n int, durs []time.Duration) time.Duration {
	if len(durs) == 0 {
		return 0
	}
	if len(durs) == 1 {
		return durs[0]
	}

	sort.Sort(durations(durs))

	v := (float64(n) / 100.0) * float64(len(durs))
	ix := int(v)
	if v-float64(int(v)) != 0 {
		if ix = round(v); ix > 0 {
			ix--
		}

		return durs[ix]
	}

	// edge cases
	if ix == 0 {
		return durs[0]
	}
	if ix == len(durs) {
		return durs[len(durs)-1]
	}

	sum := durs[ix] + durs[ix-1]
	return sum / 2
}

// Copied from effective Go : https://golang.org/doc/effective_go.html#constants
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

type byteSize float64

const (
	_           = iota // ignore first value by assigning to blank identifier
	kb byteSize = 1 << (10 * iota)
	mb
	gb
	tb
	pb
	eb
	zb
	yb
)

func (b byteSize) String() string {
	cmp := b
	if b < 0 {
		cmp = -cmp
	}
	switch {
	case cmp >= yb:
		return fmt.Sprintf("%.2fYB", b/yb)
	case cmp >= zb:
		return fmt.Sprintf("%.2fZB", b/zb)
	case cmp >= eb:
		return fmt.Sprintf("%.2fEB", b/eb)
	case cmp >= pb:
		return fmt.Sprintf("%.2fPB", b/pb)
	case cmp >= tb:
		return fmt.Sprintf("%.2fTB", b/tb)
	case cmp >= gb:
		return fmt.Sprintf("%.2fGB", b/gb)
	case cmp >= mb:
		return fmt.Sprintf("%.2fMB", b/mb)
	case cmp >= kb:
		return fmt.Sprintf("%.2fKB", b/kb)
	}
	return fmt.Sprintf("%.2fB", b)
}

type templateStats struct {
	Run       *runStats
	Before    *expVars
	After     *expVars
	Latencies []time.Duration
}

type runStats struct {
	Addr     string
	Protocol string
	URI      string
	NURIs    int
	Payload  string

	Conns          int
	Rate           time.Duration
	Timeout        time.Duration
	Duration       time.Duration
	ActualDuration time.Duration

	Calls int64
	Ack   int64
	Nack  int64
	Res   int64
	Exp   int64
}

type expVars struct {
	Juggler struct {
		ActiveConnGoros    int
		ActiveConns        int
		MsgsCALL           int
		MsgsNACK           int
		Msgs               int
		MsgsACK            int
		MsgsRead           int
		RecoveredPanics    int
		MsgsRES            int
		SlowProcessMsg     int
		SlowProcessMsgCALL int
		SlowProcessMsgNACK int
		SlowProcessMsgACK  int
		SlowProcessMsgRES  int
		TotalConnGoros     int
		TotalConns         int
		MsgsWrite          int
	}

	Memstats struct {
		Alloc        byteSize
		TotalAlloc   byteSize
		Mallocs      int
		Frees        int
		HeapAlloc    byteSize
		HeapInuse    byteSize
		HeapObjects  int
		StackInuse   byteSize
		NumGC        int
		PauseTotalNs time.Duration
	}
}

func main() {
	flag.Parse()
	if *helpFlag {
		flag.Usage()
		return
	}

	log.SetFlags(0)

	if *connFlag <= 0 {
		log.Fatalf("invalid -c value, must be greater than 0")
	}

	<-time.After(*delayFlag)
	rand.Seed(time.Now().UnixNano())

	stats := &runStats{
		Addr:     *addrFlag,
		Protocol: *subprotoFlag,
		URI:      *uriFlag,
		NURIs:    *numURIsFlag,
		Payload:  *payloadFlag,
		Conns:    *connFlag,
		Rate:     *callRateFlag,
		Timeout:  *callTimeoutFlag,
		Duration: *durationFlag,
	}

	parsed, err := url.Parse(stats.Addr)
	if err != nil {
		log.Fatalf("failed to parse --addr: %v", err)
	}
	parsed.Scheme = "http"
	parsed.Path = "/debug/vars"
	before := getExpVars(parsed)

	clientStarted := make(chan struct{})
	resLatency := make(chan []time.Duration)
	stop := make(chan struct{})
	for i := 0; i < stats.Conns; i++ {
		go runClient(stats, clientStarted, stop, resLatency)
	}

	// start clients with some jitter, up to 10ms
	log.Printf("%d connections started...", stats.Conns)
	start := time.Now()
	for i := 0; i < stats.Conns; i++ {
		<-time.After(time.Duration(rand.Intn(int(10 * time.Millisecond))))
		<-clientStarted
	}

	// run for the requested duration and signal stop
	<-time.After(stats.Duration)
	close(stop)
	log.Printf("stopping...")

	// wait for completion
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
			return
		case <-time.After(*waitFlag):
			log.Fatalf("failed to stop clients")
		}
	}()

	var latencies []time.Duration
	for i := 0; i < stats.Conns; i++ {
		latencies = append(latencies, <-resLatency...)
	}
	close(done)

	end := time.Now()
	stats.ActualDuration = end.Sub(start)
	log.Printf("stopped.")

	after := getExpVars(parsed)

	ts := templateStats{Run: stats, Before: before, After: after, Latencies: latencies}
	if err := tpl.Execute(os.Stdout, ts); err != nil {
		log.Fatalf("template.Execute failed: %v", err)
	}
}

func getExpVars(u *url.URL) *expVars {
	res, err := http.Get(u.String())
	if err != nil {
		log.Fatalf("failed to fetch /debug/vars: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		log.Fatalf("failed to fetch /debug/vars: %d %s", res.StatusCode, res.Status)
	}

	var ev expVars
	if err := json.NewDecoder(res.Body).Decode(&ev); err != nil {
		log.Fatalf("failed to decode expvars: %v", err)
	}
	return &ev
}

func getURI(stats *runStats) string {
	uri := stats.URI
	if stats.NURIs > 0 {
		n := rand.Intn(stats.NURIs)
		uri += "." + strconv.Itoa(n)
	}
	return uri
}

func runClient(stats *runStats, started chan<- struct{}, stop <-chan struct{}, resLatencies chan<- []time.Duration) {
	var wgResults sync.WaitGroup
	var mu sync.Mutex // protects latencies slice and startTimes map
	var latencies []time.Duration
	startTimes := make(map[string]time.Time)

	cli, err := client.Dial(
		&websocket.Dialer{Subprotocols: []string{stats.Protocol}},
		stats.Addr, nil,
		client.SetLogFunc(juggler.DiscardLog),
		client.SetHandler(client.HandlerFunc(func(ctx context.Context, c *client.Client, m message.Msg) {
			switch m.Type() {
			case message.ResMsg:
				rm := m.(*message.Res)
				mu.Lock()
				dur := time.Now().Sub(startTimes[rm.Payload.For.String()])
				latencies = append(latencies, dur)
				mu.Unlock()
				atomic.AddInt64(&stats.Res, 1)
			case client.ExpMsg:
				atomic.AddInt64(&stats.Exp, 1)
			case message.AckMsg:
				atomic.AddInt64(&stats.Ack, 1)
				return
			case message.NackMsg:
				atomic.AddInt64(&stats.Nack, 1)
			default:
				log.Fatalf("unexpected message type %s", m.Type())
			}
			wgResults.Done()
		})))

	if err != nil {
		log.Fatalf("Dial failed: %v", err)
	}

	var after time.Duration
	started <- struct{}{}
loop:
	for {
		select {
		case <-stop:
			break loop
		case <-time.After(after):
		}

		wgResults.Add(1)
		atomic.AddInt64(&stats.Calls, 1)
		uid, err := cli.Call(getURI(stats), stats.Payload, stats.Timeout)
		if err != nil {
			log.Fatalf("Call failed: %v", err)
		}
		mu.Lock()
		startTimes[uid.String()] = time.Now()
		mu.Unlock()
		after = stats.Rate
	}
	// wait for sent calls to return or expire
	wgResults.Wait()

	if err := cli.Close(); err != nil {
		log.Fatalf("Close failed: %v", err)
	}
	resLatencies <- latencies
}
