#!/bin/bash
set -euo pipefail

# delete all droplets created by create-do-cluster
doctl compute droplet delete \
    redis-cluster1 redis-cluster2 redis-cluster3 \
    juggler-client juggler-callee juggler-server

echo "6 droplets deleted."


