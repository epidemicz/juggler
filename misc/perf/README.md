# Performance tests

Tests are run on Digital Ocean droplets running Ubuntu 14.04.4 64-bit, using various configurations controlled by environment variables JUGGLER_DO_SIZE, JUGGLER_DO_REGION and JUGGLER_DO_SSHKEY. Droplets are used like this:

- 1 droplet to run Redis 3.0.7 (as a One-Click App)
- 1 droplet to run juggler-server
- 1 droplet to run the juggler-callee instance(s)
- 1 droplet to run the juggler-load tool

In cluster mode, 3 droplets are used for Redis.

Redis, the juggler-server and the juggler-callees are stopped and restarted before each test.

The `run-do-test.sh` bash script is used to automate running the load tests and collecting the results.

