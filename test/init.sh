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
	docker run -d --name natkit-mock --network "$NET" -e "NO_DESCRIBE=${NO_DESCRIBE:-}" -v "$PWD/test/mock.py:/mock.py:ro" \
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
	run "$1" "if natkit-init >/dev/null 2>&1; then exit 1; fi; grep -qF \"natkit: FAIL $2\" /tmp/console" ||
		{ echo "FAIL: not refused with '$2': $1"; exit 1; }
}
calls='curl -fsS http://$MOCK:8080/_calls' # $MOCK: expanded in the container

echo "== no user-data: masquerade out of eth0, nothing else"
run "" "natkit-init
	[ \"\$(cat /tmp/console)\" = 'natkit: ready, public eth0 private eth0, eip none, nat64 off, cwagent off' ]
	nft list table ip natkit | grep -q 'oifname \"eth0\" meta l4proto { tcp, udp } masquerade to :1024-61000'
	nft list table ip natkit | grep -q 'oifname \"eth0\" masquerade\$'
	nft list table inet natkit | grep -q 'hook input priority filter; policy drop;'
	nft list table inet natkit | grep -q 'ct state invalid drop'
	nft list chain inet natkit forward | grep -q 'hook forward priority filter; policy accept;'
	if nft list chain inet natkit prerouting >/dev/null 2>&1; then exit 1; fi
	[ \$(cat /proc/sys/net/ipv4/conf/eth0/send_redirects) = 0 ]
	[ \$(cat /proc/sys/net/ipv4/ip_forward) = 1 ] && [ \$(cat /proc/sys/net/ipv6/conf/all/forwarding) = 1 ]
	[ \$(cat /proc/sys/net/ipv4/conf/all/accept_redirects) = 0 ]
	[ \$(cat /proc/sys/net/ipv4/conf/all/rp_filter) = 0 ] && [ \$(cat /proc/sys/net/ipv4/conf/eth0/rp_filter) = 0 ]
	[ \"\$(cat /run/natkit/getty.shadow)\" = 'root:*::0:99999:7:::' ]
	[ \"\$($calls)\" = '[]' ] && [ ! -e /run/natkit/cwagent ]"

echo "== everything: EIP, static ENI (taken over from an old instance), tuning, CloudWatch agent, console password"
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
	[ \"\$($calls | yq -p json -o json -I0 'map(.Action)')\" = '[\"ModifyNetworkInterfaceAttribute\",\"DescribeNetworkInterfaces\",\"DetachNetworkInterface\",\"AttachNetworkInterface\",\"AttachNetworkInterface\",\"AssociateAddress\"]' ]
	[ \"\$(tr -s '\t ' ' ' </proc/sys/net/ipv4/ip_local_port_range)\" = '2048 60000' ]
	[ \$(cat /proc/sys/net/netfilter/nf_conntrack_tcp_timeout_established) = 3600 ]
	[ \$(cat /proc/sys/net/ipv4/tcp_keepalive_time) = 600 ] && [ \$(cat /proc/sys/net/ipv4/tcp_max_syn_backlog) = 4096 ]
	[ \$(yq -p json .metrics.namespace /run/natkit/cwagent/cwagentconfig.json) = natkit ]
	[ \"\$(cat /run/natkit/getty.shadow)\" = 'root:\$6\$salt\$hash::0:99999:7:::' ] && [ \$(stat -c %a /run/natkit/getty.shadow) = 600 ]
	nft list table ip natkit | grep -q 'oifname \"eth0\" masquerade'"

echo "== static ENI without the permission to look it up: no takeover, waits for it"
NO_DESCRIBE=1 run 'eni_id: eni-0bbbbbbbbbbbbbbbb' "ip link add eth9 address 02:42:ac:11:00:98 type dummy
	natkit-init >/tmp/log
	grep -q \"can't look up eni-0bbbbbbbbbbbbbbbb's attachment: waiting for it\" /tmp/log
	[ \"\$(cat /tmp/console)\" = 'natkit: ready, public eth0 private eth9, eip none, nat64 off, cwagent off' ]
	[ \"\$($calls | yq -p json -o json -I0 'map(.Action)')\" = '[\"ModifyNetworkInterfaceAttribute\",\"DescribeNetworkInterfaces\",\"AttachNetworkInterface\",\"AttachNetworkInterface\"]' ]"

echo "== interface: masquerade out of another interface"
run 'interface: eth9' "ip link add eth9 type dummy
	natkit-init
	[ \"\$(cat /tmp/console)\" = 'natkit: ready, public eth9 private eth9, eip none, nat64 off, cwagent off' ]
	nft list table ip natkit | grep -q 'oifname \"eth9\" masquerade'"

echo "== client_cidrs: new connections only from the clients (overlaps merged), NAT64 only from IPv6 clients"
run 'client_cidrs: [10.0.0.0/16, 10.0.1.0/24, "2600:1f18:aaaa:bb00::/56"]' "natkit-init
	nft list chain inet natkit forward | grep -q 'hook forward priority filter; policy drop;'
	nft list chain inet natkit forward | grep -q 'ct state established,related accept'
	nft list chain inet natkit forward | grep -q 'ip saddr @clients4 accept'
	nft list chain inet natkit forward | grep -q 'ip6 saddr @clients6 accept'
	nft list set inet natkit clients4 | grep -q 'elements = { 10.0.0.0/16 }'
	nft list set inet natkit clients6 | grep -q 'elements = { 2600:1f18:aaaa:bb00::/56 }'
	nft list chain inet natkit prerouting | grep -q 'hook prerouting priority raw; policy accept;'
	nft list chain inet natkit prerouting | grep -q 'ip6 daddr 64:ff9b::/96 ip6 saddr != @clients6 drop'"

echo "== client_cidrs, IPv4 only: an empty IPv6 set, so no NAT64 for anyone"
run 'client_cidrs: [192.168.0.0/24]' "natkit-init
	nft list set inet natkit clients4 | grep -q 'elements = { 192.168.0.0/24 }'
	if nft list set inet natkit clients6 | grep -q elements; then exit 1; fi"

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
fails 'client_cidrs: 10.0.0.0/16' "user-data: client_cidrs: not a list of CIDRs"
fails 'client_cidrs: []' "user-data: client_cidrs: not a list of CIDRs"
fails 'client_cidrs: [10.0.0.0]' "user-data: client_cidrs: bad value '10.0.0.0'"
fails 'client_cidrs: ["10.0.0.0/16; flush ruleset"]' "user-data: client_cidrs: bad value '10.0.0.0/16;'"
fails 'client_cidrs: [999.0.0.0/8]' "user-data: client_cidrs: bad value '999.0.0.0/8'"
fails 'client_cidrs: [010.0.0.0/8]' "user-data: client_cidrs: bad value '010.0.0.0/8'"
fails 'client_cidrs: [10.0.0.0/33]' "user-data: client_cidrs: bad value '10.0.0.0/33'"
fails 'client_cidrs: [""]' "user-data: client_cidrs: not a list of CIDRs"
fails 'client_cidrs: ["2600:1f18::/129"]' "user-data: client_cidrs: bad value '2600:1f18::/129'"
fails 'client_cidrs: ["2600:zz::/56"]' "user-data: client_cidrs: bad value '2600:zz::/56'"
fails 'eip_id: eipalloc-0bad' "AssociateAddress eipalloc-0bad failed: natkit-init: AssociateAddress: HTTP 400 InvalidParameterValue: unexpected"
fails '- a list' "user-data is not a YAML map"

echo "== a setup that fails takes neither the ENI nor the EIP"
run 'eip_id: eipalloc-0123
eni_id: eni-0bbbbbbbbbbbbbbbb
console_password: /natkit/test/plain' "ip link add eth9 address 02:42:ac:11:00:98 type dummy
	if natkit-init >/dev/null 2>&1; then exit 1; fi
	grep -qF 'natkit: FAIL console_password' /tmp/console
	[ \"\$($calls)\" = '[]' ]"

echo "== a failed setup still leaves the getty a (locked) shadow file"
run 'bogus: 1' "if natkit-init 2>/dev/null; then exit 1; fi; [ \"\$(cat /run/natkit/getty.shadow)\" = 'root:*::0:99999:7:::' ]"

echo "== the jool tool matches the modules' version"
docker run --rm --entrypoint jool "$IMG" --version | grep -q '4\.1\.15' || { echo "FAIL: jool tool isn't 4.1.15"; exit 1; }
echo "ok"
