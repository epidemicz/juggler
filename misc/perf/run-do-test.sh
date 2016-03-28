#!/bin/bash
set -euo pipefail

function finish {
    popd
}

# set cmd to $1 or an empty value if not set
cmd=${1:-}

if [ "$cmd" == "start" ]
then
    pushd ../..
    trap finish EXIT

    # build juggler for linux-amd64
    GOOS=linux GOARCH=amd64 make

    # create the redis droplet
    doctl compute droplet create \
        redis \
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

    # get the redis IP address
    redisip=$(doctl compute droplet list --format PublicIPv4 --no-header redis | head -n 1)
    echo "redis IP: " ${redisip}

    # start redis on the expected port and with the right config
    ssh -n -f root@${redisip} "sh -c 'pkill redis-server; nohup redis-server --port 9000 > /dev/null 2>&1 &'"

    exit 0
fi

if [ "$cmd" == "stop" ]
then
    doctl compute droplet delete \
        redis \
        juggler-client juggler-callee juggler-server

    exit 0
fi

echo "Usage: $0 [start|stop]"
echo "start       -- Launch droplets and run load test."
echo "stop        -- Destroy droplets."

