# performance analysis

## redis tests

All tests run for 10s, locally on MbP 2015.

1. Pure redis LPUSH and BRPOP in 2 separate goroutines and static string payload:
    `push: 183200, pop: 183200`
2. Same, marshal a CallPayload on push:
    `push: 142209, pop: 142209`
3. Same, unmarshal in a CallPayload on pop:
    `push: 129928, pop: 129928`
4. With callOrResScript with TTL on push:
    `push: 102679, pop: 102679`
5. With delAndPTTLScript on pop:
    `push: 102259, pop: 63886`
6. juggler-direct-call test with 100 listener goroutines on results, 100 workers on callees, log redirected to /dev/null:
    `calls: 25540, results: 25540, timeout: 1s`
7. Pure redis with N push and N pop goroutines (tops at around 5-10 goroutines, using more doesn't yield better throughput):
    `push: 337672, pop: 337672`

On smallest DO droplet (512mb):

1. Pure redis:
    `push: 132694, pop: 132694`
2. Marshal and unmarshal CallPayload:
    `push: 106167, pop: 106167`
3. Scripts on both push and pop:
    `push: 71255, pop: 42670`
4. Pure redis with N goros:
    `push: 199641, pop: 199641`

## DO load tests

On the smallest DO droplet (512mb), a single callee with a single goroutine comfortably serves 500 RPC calls per second:

```
--- CLIENT STATISTICS

Actual Duration: 10.603119982s
Calls:           5154
Acks:            5154
Nacks:           0
Results:         5154
Expired:         0

--- CLIENT LATENCIES

Minimum:         2.741771ms
Maximum:         80.99834ms
Average:         11.186134ms
Median:          8.497609ms
75th Percentile: 14.185625ms
90th Percentile: 21.62866ms
99th Percentile: 39.878127ms
```

```
--- CLIENT STATISTICS

Actual Duration: 30.628696972s
Calls:           15075
Acks:            15075
Nacks:           0
Results:         15075
Expired:         0

--- CLIENT LATENCIES

Minimum:         4.445µs
Maximum:         228.451962ms
Average:         32.464283ms
Median:          26.382463ms
75th Percentile: 44.341817ms
90th Percentile: 63.209628ms
99th Percentile: 119.654373ms
```

Always on the smallest DO droplet, 2 workers for a single callee comfortably scales to twice as many calls per second, 1000:

```
--- CLIENT STATISTICS

Actual Duration: 10.736635356s
Calls:           10113
Acks:            10113
Nacks:           0
Results:         10113
Expired:         0

--- CLIENT LATENCIES

Minimum:         1.766395ms
Maximum:         158.620289ms
Average:         24.497455ms
Median:          19.706047ms
75th Percentile: 31.693501ms
90th Percentile: 45.733072ms
99th Percentile: 107.996045ms
```

```
--- CLIENT STATISTICS

Actual Duration: 30.606733281s
Calls:           28885
Acks:            28885
Nacks:           0
Results:         28885
Expired:         0

--- CLIENT LATENCIES

Minimum:         270.82µs
Maximum:         309.058338ms
Average:         71.484621ms
Median:          61.445101ms
75th Percentile: 86.376518ms
90th Percentile: 130.361248ms
99th Percentile: 229.321435ms
```

4 workers with 200 clients at the same 100ms rate almost scales linearly to 2000 calls per second (note: did not work at first, had to set -redis-max-idle=100 on server and callees):

```
--- CLIENT STATISTICS

Actual Duration: 11.274830119s
Calls:           18815
Acks:            18815
Nacks:           0
Results:         18815
Expired:         0

--- CLIENT LATENCIES

Minimum:         9.76µs
Maximum:         487.388799ms
Average:         109.66052ms
Median:          96.374854ms
75th Percentile: 139.303215ms
90th Percentile: 187.284696ms
99th Percentile: 347.435284ms
```

Same load over 6 workers doesn't make it better.

```
--- CLIENT STATISTICS

Actual Duration: 34.033306295s
Calls:           19381
Acks:            19381
Nacks:           0
Results:         19371
Expired:         10

--- CLIENT LATENCIES

Minimum:         2.797945ms
Maximum:         454.385784ms
Average:         160.171314ms
Median:          146.159373ms
75th Percentile: 226.037951ms
90th Percentile: 280.015634ms
99th Percentile: 383.518786ms
```

With the -1s rate (auto-regulated clients, next call is sent only when response from previous call is received), it also tops at around 2000 calls per second:

```
--- CLIENT STATISTICS

Actual Duration: 11.432001962s
Calls:           19612
Acks:            19612
Nacks:           0
Results:         19612
Expired:         0

--- CLIENT LATENCIES

Minimum:         6.493948ms
Maximum:         354.051851ms
Average:         108.678899ms
Median:          107.953711ms
75th Percentile: 122.757087ms
90th Percentile: 150.326661ms
99th Percentile: 252.28389ms
```
