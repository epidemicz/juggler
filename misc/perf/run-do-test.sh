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

function badFlags {
    echo "error: unknown or invalid option $1."
    exit 3
}

# set cmd to $1 or an empty value if not set
cmd=${1:-}

# start command
if [[ ${cmd} == "start" ]] || [[ ${cmd} == "debug" ]]; then
    # parse command-line flags
    ncallees=1
    nclients=100
    nworkersPerCallee=1
    nuris=0
    callRate=100ms
    duration=10s
    payload=100
    timeout=1m
    waitForStart=10s
    waitForEnd=10s
    cluster=0
    noDroplet=0

    shift # the command name
    while [[ $# > 0 ]]; do
        key=$1

        case $key in
        -c|--callees|-C|--clients|-d|--duration|-p|--payload|-r|--rate|-t|--timeout|-u|--uris|--wait1|--wait2|-w|--workers)
            if [[ $# == 1 ]]; then
                badFlags ${key}
            fi
            shift
            ;;& # continue matching $key

        -c|--callees)
            ncallees=$1
            ;;

        --cluster)
            cluster=1
            ;;

        -C|--clients)
            nclients=$1
            ;;

        -d|--duration)
            duration=$1
            ;;

        -D|--no-droplet)
            noDroplet=1
            ;;

        -p|--payload)
            payload=$1
            ;;

        -r|--rate)
            callRate=$1
            ;;

        -t|--timeout)
            timeout=$1
            ;;

        -u|--uris)
            nuris=$1
            ;;

        --wait1)
            waitForStart=$1
            ;;

        --wait2)
            waitForEnd=$1
            ;;

        -w|--workers)
            nworkersPerCallee=$1
            ;;

        *)
            badFlags ${key}
            ;;
        esac
        shift
    done


    if [[ ${cmd} == "debug" ]]; then
        echo "--callees ${ncallees} --clients ${nclients} --workers ${nworkersPerCallee}" \
            "--uris ${nuris} --rate ${callRate} --duration ${duration} --payload ${payload}" \
            "--timeout ${timeout} --wait1 ${waitForStart} --wait2 ${waitForEnd}" \
            "--no-droplet ${noDroplet} --cluster ${cluster}"
        exit 199
    fi

    pushd ../..
    trap finish EXIT

    # build juggler for linux-amd64
    GOOS=linux GOARCH=amd64 make

    if [[ ${noDroplet} == 0 ]]; then
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
    fi

    # grab the IP addresses of all droplets
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

        if [[ ${ip} == "" ]]; then
            echo "error: missing droplet ${droplet}."
            exit 4
        fi

        ssh-keygen -R ${ip}
        # keyscan doesn't work reliably for some reason, adds only the redis one?
        ssh-keyscan -t ecdsa ${ip} >> ${HOME}/.ssh/known_hosts
        dropletIPs[${droplet}]=${ip}
        sleep 1
    done

    # start redis on the expected port and with the right config
    echo
    echo "redis IP: " ${dropletIPs["juggler-redis"]}
    ssh root@${dropletIPs["juggler-redis"]} \
        "sh -c 'pkill redis-server; echo 511 > /proc/sys/net/core/somaxconn'"
    ssh -n -f root@${dropletIPs["juggler-redis"]} \
        "sh -c 'nohup redis-server --port 7000 --maxclients 100000 > /dev/null 2>&1 &'"

    # copy the server to juggler-server
    echo
    echo "server IP: " ${dropletIPs["juggler-server"]}
    ssh -n -f root@${dropletIPs["juggler-server"]} "sh -c 'pkill juggler-server'"
    scp -C juggler-server root@${dropletIPs["juggler-server"]}:~
    ssh -n -f root@${dropletIPs["juggler-server"]} \
        "sh -c 'nohup ~/juggler-server -L -redis=${dropletIPs["juggler-redis"]}:7000 > /dev/null 2>&1 &'"

    # copy the callee to juggler-callee
    echo
    echo "callee IP: " ${dropletIPs["juggler-callee"]}
    ssh -n -f root@${dropletIPs["juggler-callee"]} "sh -c 'pkill juggler-callee'"
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

    # run the load test
    echo
    echo "starting test..."
    ssh root@${dropletIPs["juggler-load"]} \
        "sh -c '~/juggler-load -addr=ws://${dropletIPs["juggler-server"]}:9000/ws -c ${nclients} -n ${nuris} -r ${callRate} -t ${timeout} -d ${duration} -p ${payload} -delay ${waitForStart} -w ${waitForEnd} > ~/juggler-load.out'"

    # retrieve the results
    outfile="misc/perf/$(date +'%Y-%m-%d_%H:%M')-c=${ncallees}-C=${nclients}-d${duration}-p${payload}-r${callRate}-t${timeout}-u${nuris}-w${nworkersPerCallee}-w1${waitForStart}-w2${waitForEnd}-cluster${cluster}-${JUGGLER_DO_SIZE}"
    scp -C root@${dropletIPs["juggler-load"]}:~/juggler-load.out ${outfile}
    echo "done."

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
echo "start [--callees N --clients N --cluster --duration T --no-droplet --payload N"
echo "       --rate T --timeout T --uris N --wait1 T --wait2 T --workers N]"
echo "            -- Launch droplets and run load test."
echo "               WARNING: will charge money!"
echo "stop        -- Destroy droplets."
echo "               WARNING: destroys droplets by name!"
echo

