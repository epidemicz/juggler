# juggler

TODO :
* opt-out of pub-sub listening and/or RPC results when connecting, for more efficient connections when it is known ahead of time that this won't be needed (will not subscribe and/or will not call). Possibly via a juggler header when connecting (Juggler-Allowed-Messages: call, sub, unsb, pub).
* document root juggler package (godoc)
* make client thread-safe.
* better test coverage
* README/License and blog article
* run something like https://github.com/mkevac/debugcharts and https://github.com/uber/go-torch and possibly https://github.com/davecheney/gcvis

