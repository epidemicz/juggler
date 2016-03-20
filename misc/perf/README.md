# Performance tests

Tests have been run on Digital Ocean droplets running Ubuntu 14.04.4 64-bit, 512MB/1 CPU, 20 GB SSD disk, 1000GB transfer in the Toronto data center. Droplets were used like this:

- 1 droplet to run Redis 3.0.7 (as a One-Click App, but configured with higher max clients - 100 000 - and TCP backlog - 511)
- 1 droplet to run juggler-server
- 1 droplet to run the juggler-callee instances
- 1 droplet to run the juggler-load tool

Redis, the juggler-server and the juggler-callees were stopped and restarted before each test.

