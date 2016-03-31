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

function popdir {
    popd
}

function badFlags {
    echo "error: unknown or invalid option $1."
    exit 3
}

function dropletNames {
    eval "$2+=(juggler-server juggler-callee juggler-load)"
    if [[ $1 == 1 ]]; then
        eval "$2+=(juggler-redis1 juggler-redis2 juggler-redis3)"
    else
        eval "$2+=(juggler-redis)"
    fi
}

function dropletIPAddrs {
    for i in $(seq 2 $#); do
        dropletName=${!i}

        getip='doctl compute droplet list --format PublicIPv4 --no-header ${dropletName} | head -n 1'
        ip=$(eval ${getip})

        if [[ ${ip} == "" ]]; then
            echo "error: missing droplet ${dropletName}."
            exit 4
        fi
        eval "$1[${dropletName}]=${ip}"
    done
}

# set cmd to $1 or an empty value if not set
cmd=${1:-}

# up command : create the droplets
if [[ ${cmd} == "up" ]]; then
    # parse command-line flags
    cluster=0
    debug=0
    shift # the command name
    while [[ $# > 0 ]]; do
        key=$1

        case $key in
        --cluster)
            cluster=1
            ;;

        --debug)
            debug=1
            ;;

        *)
            badFlags ${key}
            ;;
        esac
        shift
    done

    if [[ ${debug} == 0 ]]; then
        redisName=(juggler-redis)
        if [[ ${cluster} == 1 ]]; then
            redisName=(juggler-redis1 juggler-redis2 juggler-redis3)
        fi
        # create the redis droplet(s)
        doctl compute droplet create \
            ${redisName[@]} \
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

    declare -a droplets
    declare -A dropletIPs
    dropletNames ${cluster} droplets
    dropletIPAddrs dropletIPs ${droplets[@]}

    if [[ ${debug} == 1 ]]; then
        for droplet in ${droplets[@]}; do
            echo ${droplet}
        done
        for key in ${!dropletIPs[@]}; do
            echo ${key} ${dropletIPs[${key}]}
        done
        exit 199
    fi

    # update the known_hosts file
    for ip in ${dropletIPs[@]}; do
        ssh-keygen -R ${ip}
        sleep .1
        # keyscan doesn't work reliably for some reason?
        ssh-keyscan -t ecdsa ${ip} >> ${HOME}/.ssh/known_hosts
        sleep .1
    done

    exit 0
fi

# make command
if [[ ${cmd} == "make" ]]; then
    pushd ../..
    trap popdir EXIT

    # build juggler for linux-amd64
    GOOS=linux GOARCH=amd64 make
    exit 0
fi

# run command : run the load test
if [[ ${cmd} == "run" ]]; then
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
    debug=0

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

        --debug)
            debug=1
            ;;

        -d|--duration)
            duration=$1
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

    # grab the IP addresses of all droplets
    declare -a droplets
    declare -A dropletIPs
    dropletNames ${cluster} droplets
    dropletIPAddrs dropletIPs ${droplets[@]}

    if [[ ${debug} == 1 ]]; then
        echo "--callees ${ncallees} --clients ${nclients} --cluster ${cluster} " \
            "--duration ${duration} --payload ${payload} --rate ${callRate} " \
            "--timeout ${timeout} --uris ${nuris} --wait1 ${waitForStart} --wait2 ${waitForEnd}" \
            "--workers ${nworkersPerCallee}"

        for droplet in ${droplets[@]}; do
            echo ${droplet}
        done
        for key in ${!dropletIPs[@]}; do
            echo ${key} ${dropletIPs[${key}]}
        done

        exit 199
    fi

    pushd ../..
    trap popdir EXIT

    # start redis on the expected port and with the right config
    if [[ ${cluster} == 1 ]]; then
        redisIP=${dropletIPs["juggler-redis1"]}
        echo
        lastNode=""
        start=0
        for ((i=1; i <= 3; i++)); do
            ip=${dropletIPs["juggler-redis"$i]}
            echo "redis IP $i: " ${ip}

            # kill running server and ensure bigger max tcp conns
            ssh root@${ip} \
                "sh -c 'pkill redis-server; echo 511 > /proc/sys/net/core/somaxconn'"

            # start server in cluster mode
            ssh -n -f root@${ip} \
                "sh -c 'nohup redis-server --port 7000 --cluster-enabled yes --cluster-config-file node.conf --cluster-node-timeout 5000 --appendonly no --maxclients 100000 > /dev/null 2>&1 &'"

            # reset the cluster info
            ssh root@${ip} \
                "sh -c 'redis-cli -p 7000 flushall; redis-cli -p 7000 cluster reset hard'"

            # add 1/3 of the slots
            end=$(( $i * (16384 / 3) ))
            if [[ $i == 3 ]]; then
                end=16383
            fi
            vals=$(seq ${start} ${end})
            ssh root@${ip} \
                "sh -c 'redis-cli -p 7000 cluster addslots " ${vals[@]} "'"
            start=$(( ${end}+1 ))

            # join the cluster
            if [[ ${lastNode} != "" ]]; then
                ssh root@${ip} \
                    "sh -c 'redis-cli -p 7000 cluster meet ${lastNode} 7000'"
            fi
            lastNode=${ip}
        done

        # wait a little for the cluster to form, before connecting to it
        sleep 5
    else
        redisIP=${dropletIPs["juggler-redis"]}
        echo
        echo "redis IP: " ${redisIP}
        ssh root@${redisIP} \
            "sh -c 'pkill redis-server; echo 511 > /proc/sys/net/core/somaxconn'"
        ssh -n -f root@${redisIP} \
            "sh -c 'nohup redis-server --port 7000 --maxclients 100000 > /dev/null 2>&1 &'"
    fi

    # copy the server to juggler-server
    serverIP=${dropletIPs["juggler-server"]}
    echo
    echo "server IP: " ${serverIP}
    ssh root@${serverIP} "sh -c 'pkill juggler-server || true'"
    scp -C juggler-server root@${serverIP}:~

    # start the juggler server
    args="-L -redis=${redisIP}:7000 -redis-max-idle=100"
    if [[ ${cluster} == 1 ]]; then
        args+=" -redis-cluster"
    fi
    ssh -n -f root@${serverIP} \
        "sh -c 'nohup ~/juggler-server " ${args} " > /dev/null 2>&1 &'"

    # copy the callee to juggler-callee
    calleeIP=${dropletIPs["juggler-callee"]}
    echo
    echo "callee IP: " ${calleeIP}
    ssh root@${calleeIP} "sh -c 'pkill juggler-callee || true'"
    scp -C juggler-callee root@${calleeIP}:~

    # start ncallees with nworkersPerCallee each
    for i in $(seq 1 $ncallees); do
        args="-n=${nuris} -port=$((9000 + $i)) -workers=${nworkersPerCallee} -redis-max-idle=100 -redis=${redisIP}:7000"
        if [[ ${cluster} == 1 ]]; then
            args+=" -redis-cluster"
        fi
        ssh -n -f root@${calleeIP} \
            "sh -c 'nohup ~/juggler-callee " ${args} " > ~/juggler-callee$i.log 2>&1 &'"
    done

    # copy the load tool to juggler-load
    loadIP=${dropletIPs["juggler-load"]}
    echo
    echo "load IP: " ${loadIP}
    scp -C juggler-load root@${loadIP}:~

    # run the load test
    echo
    echo "starting test..."
    ssh root@${loadIP} \
        "sh -c '~/juggler-load -addr=ws://${serverIP}:9000/ws -c ${nclients} -n ${nuris} -r ${callRate} -t ${timeout} -d ${duration} -p ${payload} -delay ${waitForStart} -w ${waitForEnd} > ~/juggler-load.out'"

    # retrieve the results
    outfile="misc/perf/$(date +'%Y-%m-%d_%H:%M')-c=${ncallees}-C=${nclients}-d${duration}-p${payload}-r${callRate}-t${timeout}-u${nuris}-w${nworkersPerCallee}-w1${waitForStart}-w2${waitForEnd}-cluster${cluster}-${JUGGLER_DO_SIZE}"
    scp -C root@${loadIP}:~/juggler-load.out ${outfile}
    echo "done."

    exit 0
fi

# down command : destroy droplets
if [[ ${cmd} == "down" ]]; then
    # parse command-line arguments
    debug=0
    shift # the command name
    while [[ $# > 0 ]]; do
        key=$1

        case $key in
        --debug)
            debug=1
            ;;

        *)
            badFlags ${key}
            ;;
        esac
        shift
    done

    if [[ ${debug} == 1 ]]; then
        names=$(doctl compute droplet list juggler-* --no-header --format Name)
        for name in ${names}; do
            echo "would delete ${name}"
        done

        if [[ ${names} == "" ]]; then
            echo "would not delete any droplet"
        fi
        exit 199
    fi

    ids=$(doctl compute droplet list juggler-* --no-header --format ID)
    for id in ${ids}; do
        doctl compute droplet delete ${id}
    done
    exit 0
fi

# invalid or no command, print usage
echo "Usage: $0 [down|help|make|run|up]"
echo
echo "down        Destroy droplets. WARNING: destroys by name!"
echo "               --debug        dry-run, print information"
echo "help        Display this message."
echo "make        Build the juggler commands."
echo "run         Execute the load test."
echo "               -c|--callees   number of callees"
echo "               --cluster      run in redis cluster mode"
echo "               -C|--clients   number of client connections"
echo "               --debug        dry-run, print information"
echo "               -d|--duration  duration of the test"
echo "               -p|--payload   payload of the calls (represents the"
echo "                              duration in ms of the RPC)"
echo "               -r|--rate      rate of requests per client"
echo "               -t|--timeout   timeout after which RPC calls expire"
echo "               -u|--uris      number of available RPC URIs"
echo "               --wait1        time to wait before starting the test"
echo "               --wait2        time to wait for the test to end properly"
echo "               -w|--workers   number of workers per callee"
echo "up          Create the droplets. WARNING: costs money!"
echo "               --cluster      create droplets for a redis cluster"
echo "               --debug        dry-run, print information"
echo

