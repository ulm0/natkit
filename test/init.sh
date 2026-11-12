#!/bin/sh
# Runs init/natkit-init against a mock IMDS + EC2 + SSM (test/mock.py) in docker, privileged (nftables, sysctls,
# a dummy interface standing in for the static ENI).  Usage: test/init.sh  (builds ghcr.io/ulm0/natkit/init:latest)
set -eu
cd "$(dirname "$0")/.."
IMG=ghcr.io/ulm0/natkit/init:latest
NET=natkit-init-test
docker build -q -t "$IMG" init >/dev/null 2>&1 || docker build --progress plain -t "$IMG" init # again, with the log
docker network create "$NET" >/dev/null
trap 'docker rm -f natkit-mock >/dev/null 2>&1; docker network rm "$NET" >/dev/null; rm -f test/.user-data.yaml' EXIT

# run <user-data or ""> <script>: the script runs natkit-init itself, then checks
run() {
	docker rm -f natkit-mock >/dev/null 2>&1 || :
	[ -z "$1" ] || printf '%s\n' "$1" >test/.user-data.yaml
	docker run -d --name natkit-mock --network "$NET" -v "$PWD/test/mock.py:/mock.py:ro" \
		${1:+-v "$PWD/test/.user-data.yaml:/user-data:ro"} python:3.13-alpine python /mock.py >/dev/null
	sleep 1
	# by IP: natkit-init's `flush ruleset` also removes docker's embedded-DNS rules in this container
	mock=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' natkit-mock)
	docker run --rm --privileged --network "$NET" --mac-address 02:42:ac:11:00:99 -e "MOCK=$mock" \
		-e "NATKIT_IMDS=http://$mock:8080/latest" -e "NATKIT_EC2=http://$mock:8080/" \
		-e "NATKIT_SSM=http://$mock:8080/" -e NATKIT_CONSOLE=/tmp/console \
		--entrypoint /bin/sh "$IMG" -c "set -e; $2"
}
# fails <user-data> <console reason>: natkit-init must exit non-zero and put the reason on the console
fails() {
	run "$1" "if natkit-init >/dev/null 2>&1; then exit 1; fi; grep -qF 'natkit: FAIL $2' /tmp/console" ||
		{ echo "FAIL: not refused with '$2': $1"; exit 1; }
}
calls='curl -fsS http://$MOCK:8080/_calls' # $MOCK: expanded in the container

echo "== no user-data: masquerade out of eth0, nothing else"
run "" "natkit-init
	[ \"\$(cat /tmp/console)\" = 'natkit: ready, public eth0 private eth0, eip none, nat64 off, cwagent off' ]
	nft list table ip natkit | grep -q 'oifname \"eth0\" masquerade'
	nft list table inet natkit | grep -q 'hook input priority filter; policy drop;'
	nft list table inet natkit | grep -q 'ct state invalid drop'
	[ \$(cat /proc/sys/net/ipv4/conf/eth0/send_redirects) = 0 ]
	[ \$(cat /proc/sys/net/ipv4/conf/all/rp_filter) = 0 ] && [ \$(cat /proc/sys/net/ipv4/conf/eth0/rp_filter) = 0 ]
	[ \"\$(cat /run/natkit/getty.shadow)\" = 'root:*::0:99999:7:::' ]
	[ \"\$($calls)\" = '[]' ] && [ ! -e /run/natkit/cwagent ]"

echo "== everything: EIP, static ENI (attached on the second try), tuning, CloudWatch agent, console password"
run 'eip_id: eipalloc-0123
eni_id: eni-0bbbbbbbbbbbbbbbb
ip_local_port_range: "2048 60000"
nf_conntrack_tcp_timeout_established: 3600
tcp_keepalive_time: 600
tcp_max_syn_backlog: 4096
cwagent_enabled: true
cwagent_cfg_param_name: /natkit/test/cw
console_password: /natkit/test/root-hash' "ip link add eth9 address 02:42:ac:11:00:98 type dummy
	natkit-init
	[ \"\$(cat /tmp/console)\" = 'natkit: ready, public eth0 private eth9, eip eipalloc-0123, nat64 off, cwagent on' ]
	ip -o link show eth9 | grep -q ',UP'
	[ \"\$($calls | yq -p json -o json -I0 'map(.Action)')\" = '[\"ModifyNetworkInterfaceAttribute\",\"AttachNetworkInterface\",\"AttachNetworkInterface\",\"AssociateAddress\"]' ]
	[ \"\$(tr -s '\t ' ' ' </proc/sys/net/ipv4/ip_local_port_range)\" = '2048 60000' ]
	[ \$(cat /proc/sys/net/netfilter/nf_conntrack_tcp_timeout_established) = 3600 ]
	[ \$(cat /proc/sys/net/ipv4/tcp_keepalive_time) = 600 ] && [ \$(cat /proc/sys/net/ipv4/tcp_max_syn_backlog) = 4096 ]
	[ \$(yq -p json .metrics.namespace /run/natkit/cwagent/cwagentconfig.json) = natkit ]
	[ \"\$(cat /run/natkit/getty.shadow)\" = 'root:\$6\$salt\$hash::0:99999:7:::' ] && [ \$(stat -c %a /run/natkit/getty.shadow) = 600 ]
	nft list table ip natkit | grep -q 'oifname \"eth0\" masquerade'"

echo "== interface: masquerade out of another interface"
run 'interface: eth9' "ip link add eth9 type dummy
	natkit-init
	[ \"\$(cat /tmp/console)\" = 'natkit: ready, public eth9 private eth9, eip none, nat64 off, cwagent off' ]
	nft list table ip natkit | grep -q 'oifname \"eth9\" masquerade'"

echo "== nat64: false is accepted (and off)"
run 'nat64: false' "natkit-init | grep -q 'NAT64: off in user-data'"

echo "== bad user-data is refused, with the reason on the console"
fails 'eip: eipalloc-0123' "user-data: unknown key 'eip'"
fails 'eip_id: eipalloc-0123; rm -rf /' "user-data: eip_id: bad value"
fails 'nf_conntrack_max: lots' "user-data: nf_conntrack_max: bad value"
fails 'nat64: maybe' "user-data: nat64: bad value"
fails 'cwagent_enabled: true' "cwagent_enabled needs cwagent_cfg_param_name"
fails 'cwagent_enabled: true
cwagent_cfg_param_name: /natkit/test/notjson' "cwagent: /natkit/test/notjson is not a JSON object"
fails 'console_password: /natkit/test/plain' "console_password: /natkit/test/plain is not a crypt hash"
fails 'interface: eth7' "interface: no interface eth7"
fails '- a list' "user-data is not a YAML map"

echo "== a failed setup still leaves the getty a (locked) shadow file"
run 'bogus: 1' "if natkit-init 2>/dev/null; then exit 1; fi; [ \"\$(cat /run/natkit/getty.shadow)\" = 'root:*::0:99999:7:::' ]"

echo "== the jool tool matches the modules' version"
docker run --rm --entrypoint jool "$IMG" --version | grep -q '4\.1\.15' || { echo "FAIL: jool tool isn't 4.1.15"; exit 1; }
echo "ok"
