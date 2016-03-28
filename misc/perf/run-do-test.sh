#!/usr/bin/env bash

# requires bash version 4+
if [[ ${BASH_VERSION} < "4" ]]
then
    echo "error: bash version 4+ is required (running ${BASH_VERSION})."
    exit 1
fi

# requires doctl command
if ! hash doctl 2> /dev/null; then
    echo "error: the doctl Digital Ocean command-line tool is required."
    exit 2
fi

# "strict mode" (see http://redsymbol.net/articles/unofficial-bash-strict-mode/)
set -euo pipefail
IFS=$'\n'

function finish {
    popd
}

# set cmd to $1 or an empty value if not set
cmd=${1:-}

# start command
if [[ ${cmd} == "start" ]]; then
    pushd ../..
    trap finish EXIT

    # parse command-line flags
    ncallees=1
    nworkersPerCallee=1
    nuris=0
    shift # the command name

    while [[ $# > 1 ]]; do
        key=$1

        case $key in
        -c|--callees)
            ncallees=$2
            shift
            ;;

        -u|--uris)
            nuris=$2
            shift
            ;;

        -w|--workers)
            nworkersPerCallee=$2
            shift
            ;;

        *)
            echo "error: unknown option ${key}."
            exit 3
            ;;
        esac
        shift
    done

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
        # keyscan doesn't work reliably for some reason, adds only the redis one?
        ssh-keyscan -t ecdsa ${ip} >> ${HOME}/.ssh/known_hosts
        dropletIPs[${droplet}]=${ip}
    done

    # start redis on the expected port and with the right config
    echo
    echo "redis IP: " ${dropletIPs["juggler-redis"]}
    ssh -n -f root@${dropletIPs["juggler-redis"]} "sh -c 'pkill redis-server; echo 511 > /proc/sys/net/core/somaxconn; nohup redis-server --port 7000 --maxclients 100000 > /dev/null 2>&1 &'"
 
    # copy the server to juggler-server
    echo
    echo "server IP: " ${dropletIPs["juggler-server"]}
    scp -C juggler-server root@${dropletIPs["juggler-server"]}:~
    ssh -n -f root@${dropletIPs["juggler-server"]} "sh -c 'nohup ~/juggler-server -L -redis=${dropletIPs["juggler-redis"]}:7000 > /dev/null 2>&1 &'"

    # copy the callee to juggler-callee
    echo
    echo "callee IP: " ${dropletIPs["juggler-callee"]}
    scp -C juggler-callee root@${dropletIPs["juggler-callee"]}:~

    # start ncallees with nworkersPerCallee each
    for i in $(seq 1 $ncallees); do
        ssh -n -f root@${dropletIPs["juggler-callee"]} \
            "sh -c 'nohup ~/juggler-callee -n=${nuris} -port=$((9000 + $i)) -workers=${nworkersPerCallee} -redis=${dropletIPs["juggler-redis"]}:7000 > /dev/null 2>&1 &'"
    done

    # copy the load tool to juggler-load
    echo
    echo "load IP: " ${dropletIPs["juggler-load"]}
    scp -C juggler-load root@${dropletIPs["juggler-load"]}:~

    exit 0
fi

# stop command
if [[ ${cmd} == "stop" ]]; then
    ids=$(doctl compute droplet list juggler-* --no-header --format ID)
    for id in ${ids}; do
        doctl compute droplet delete ${id}
    done
    exit 0
fi

# invalid or no command, print usage
echo "Usage: $0 [start|stop|help]"
echo
echo "help        -- Display this message."
echo "start [--callees N --uris N --workers N]"
echo "            -- Launch droplets and run load test."
echo "               WARNING: will charge money!"
echo "stop        -- Destroy droplets."
echo "               WARNING: destroys droplets by name!"
echo

