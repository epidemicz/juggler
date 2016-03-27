#!/bin/bash
set -euo pipefail

# create the redis droplets
doctl compute droplet create \
    redis-cluster1 redis-cluster2 redis-cluster3 \
    --image redis \
    --region ${JUGGLER_DO_REGION} \
    --size ${JUGGLER_DO_SIZE} \
    --ssh-keys ${JUGGLER_DO_SSHKEY} \
    --wait

# create the client, callee and server droplet
doctl compute droplet create \
    juggler-client juggler-callee juggler-server \
    --image ubuntu-14-04-x64 \
    --region ${JUGGLER_DO_REGION} \
    --size ${JUGGLER_DO_SIZE} \
    --ssh-keys ${JUGGLER_DO_SSHKEY} \
    --wait

echo "6 droplets created."

