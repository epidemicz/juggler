#!/usr/bin/env bash

# WARNING : requires bash version 4+

set -euo pipefail
IFS=$'\n'

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
        juggler-redis \
        --image redis \
        --region ${JUGGLER_DO_REGION} \
        --size ${JUGGLER_DO_SIZE} \
        --ssh-keys ${JUGGLER_DO_SSHKEY} \
        --wait

    # create the client, callee and server droplet
    doctl compute droplet create \
        juggler-server juggler-callee juggler-load \
        --image ubuntu-14-04-x64 \
        --region ${JUGGLER_DO_REGION} \
        --size ${JUGGLER_DO_SIZE} \
        --ssh-keys ${JUGGLER_DO_SSHKEY} \
        --wait

    droplets=(
        "juggler-redis"
        "juggler-server"
        "juggler-callee"
        "juggler-load"
    )
    declare -A dropletIPs
    for droplet in ${droplets[@]}; do
        getip='doctl compute droplet list --format PublicIPv4 --no-header ${droplet} | head -n 1'
        ip=$(eval ${getip})
        ssh-keygen -R ${ip}
        # ssh-keyscan -H ${ip} >> ~/.ssh/known_hosts
        dropletIPs[${droplet}]=${ip}
    done

    # start redis on the expected port and with the right config
    echo "redis IP: " ${dropletIPs["juggler-redis"]}
    ssh -n -f root@${dropletIPs["juggler-redis"]} "sh -c 'pkill redis-server; echo 511 > /proc/sys/net/core/somaxconn; nohup redis-server --port 7000 --maxclients 100000 > /dev/null 2>&1 &'"
 
    # copy the server to juggler-server
    echo "server IP: " ${dropletIPs["juggler-server"]}
    scp -C juggler-server root@${dropletIPs["juggler-server"]}:~
    ssh -n -f root@${dropletIPs["juggler-server"]} "sh -c 'nohup ~/juggler-server -L -redis=${dropletIPs["juggler-redis"]}:7000 > /dev/null 2>&1 &'"

    # copy the callee to juggler-callee
    echo "callee IP: " ${dropletIPs["juggler-callee"]}
    scp -C juggler-callee root@${dropletIPs["juggler-callee"]}:~
    ssh -n -f root@${dropletIPs["juggler-callee"]} "sh -c 'nohup ~/juggler-callee -redis=${dropletIPs["juggler-redis"]}:7000 > /dev/null 2>&1 &'"

    # copy the load tool to juggler-load
    echo "load IP: " ${dropletIPs["juggler-load"]}
    scp -C juggler-load root@${dropletIPs["juggler-load"]}:~

    exit 0
fi

if [ "$cmd" == "stop" ]
then
    ids=$(doctl compute droplet list juggler-* --no-header --format ID)
    for id in ${ids}; do
        doctl compute droplet delete ${id}
    done
    exit 0
fi

echo "Usage: $0 [start|stop]"
echo "WARNING: requires bash 4+"
echo
echo "start       -- Launch droplets and run load test."
echo "               WARNING: will charge money!"
echo "stop        -- Destroy droplets."
echo "               WARNING: destroys droplets by name!"
echo

