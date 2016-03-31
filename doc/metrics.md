# juggler metrics

The `juggler.Server` and the `redisbroker.Broker` types both have a `Vars` field that can be set to an `expvar.Map` to collect metrics.

## server metrics

On the server, the following metrics are collected:

* RecoveredPanics : incremented when a panic is recovered in the `juggler.PanicRecover` handler.
* Msgs : incremented for each message sent or received by the server in `juggler.ProcessMessage`.
* MsgsRead : incremented for each read message received by the server in `juggler.ProcessMessage`.
* MsgsWrite : incremented for each write message sent by the server in `juggler.ProcessMessage`.
* MsgsCALL : incremented for each CALL message received by the server in `juggler.ProcessMessage`.
* MsgsPUB : incremented for each PUB message received by the server in `juggler.ProcessMessage`.
* MsgsSUB : incremented for each SUB message received by the server in `juggler.ProcessMessage`.
* MsgsUNSB : incremented for each UNSB message received by the server in `juggler.ProcessMessage`.
* MsgsNACK : incremented for each NACK message sent by the server in `juggler.ProcessMessage`.
* MsgsACK : incremented for each ACK message sent by the server in `juggler.ProcessMessage`.
* MsgsRES : incremented for each RES message sent by the server in `juggler.ProcessMessage`.
* MsgsEVNT : incremented for each EVNT message sent by the server in `juggler.ProcessMessage`.
* MsgsUnknown : incremented for each unknown message type in `juggler.ProcessMessage`.
* SlowProcessMsg : incremented for each message that takes more than `juggler.SlowProcessMsgThreshold` to complete in `juggler.ProcessMessage`.
* SlowProcessMsg${TYPE} : same for each message type.
* ActiveConns : number of currently active connections on the server.
* TotalConns : total number of connections served by the server.
* ActiveConnGoros : number of currently active connection goroutines (a single connection may start many goroutines).
* TotalConnGoros : total number of connection goroutines executed.

## broker metrics

The broker collects the following metrics. Because the broker can be used by the server and by the callees, some metrics are exposed by the server process and other by each callee.

**Callee metrics**

* FailedCallPayloadUnmarshals : incremented when the call payload returned by redis cannot be unmarshaled.
* FailedPTTLCalls : incremented when the call to read the time-to-live of an RPC call failed.
* ExpiredCalls : incremented when an RPC call is dropped (not sent to the callee) because it has expired.
* Calls : incremented when a call payload is successfully sent over the calls channel to a callee.

**Server metrics**

* FailedEvntPayloadUnmarshals : incremented when the event payload triggered by redis pub-sub cannot be unmarshaled.
* Events : incremented when an event payload is successfully sent over the events channel to a client.
* FailedResPayloadUnmarshals : incremented when the result payload returned by redis cannot be unmarshaled.
* FailedPTTLResults : incremented when the call to read the time-to-live of an RPC result failed.
* ExpiredResults : incremented when an RPC result is dropped (not sent to the client) because it has expired.
* Results : incremented when a result payload is successfully sent over the results channel to a client.

