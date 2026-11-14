#!/bin/sh
# Module test: tofu/nat (test/module) end to end with a natkit AMI, watched through a client in the private subnet
# that prints its IPv4 and NAT64 egress on its serial console every 2 s:
#  1. the defaults: the client's egress is the EIP, IPv4 and NAT64; the public security group admits nothing, the
#     private one the VPC's CIDRs
#  2. handover: a user-data change refreshes the instance. The replacement launches first and takes the ENI over:
#     the client may be without egress for at most NATKIT_HANDOVER_MAX seconds (default 30)
#  3. a replacement whose setup fails (a setting natkit refuses): it never completes the group's lifecycle hook,
#     so the group keeps the old instance and the client's egress never stops; a good setting then goes through
#  4. conntrack alarm: natkit publishes the allowance counters under the module's name, and the alarm exists
#  5. client_cidrs without the client's subnet, untracked: nothing gets out, IPv4 or NAT64 (natkit's nftables alone)
#  6. the default client_cidrs, untracked, tcp_established_timeout 350: egress again, both ENIs at 350 s
# Then it destroys everything. The state is s3://$NATKIT_BUCKET/module-test/<tag>.tfstate, so a run that was killed
# can be destroyed from another machine (CI's boot-cleanup does).
#
#   scripts/module-test.sh <ami-id> <arm64|amd64>
#
# Needs tofu (or TF=terraform), AWS credentials + AWS_REGION, NATKIT_BUCKET and the boot test's
# NATKIT_TEST_PUBLIC_SUBNET, NATKIT_TEST_PRIVATE_SUBNET and NATKIT_TEST_INSTANCE_PROFILE (scripts/boot-test.sh).
# One at a time, and not while a boot test runs: both route the private subnet. It waits (up to 30 min) while the
# subnet's route table has a default route, another test's.
set -eu
cd "$(dirname "$0")/.."
TAG=${NATKIT_MODULE_TEST_TAG:-$1}
MAX=${NATKIT_HANDOVER_MAX:-30}
TF=${TF:-tofu}
export TF_VAR_ami_id="$1" TF_VAR_arch="$2" TF_VAR_tag="$TAG" TF_VAR_public_subnet="${NATKIT_TEST_PUBLIC_SUBNET:?}" \
	TF_VAR_private_subnet="${NATKIT_TEST_PRIVATE_SUBNET:?}" TF_VAR_instance_profile="${NATKIT_TEST_INSTANCE_PROFILE:?}"
KEY=module-test/$TAG.tfstate
L=$(mktemp -d)

tf() { "$TF" -chdir=test/module "$@" -input=false -no-color; }
out() { "$TF" -chdir=test/module output -raw "$1"; }
# apply <stage>: with the TF_VAR_s exported so far; quiet unless it fails
apply() { tf apply -auto-approve >"$L/apply-$1.log" 2>&1 || { tail -n 30 "$L/apply-$1.log" >&2; die "apply ($1)"; }; }
ago() { t=$(($(date +%s) - $1)); date -u -d "@$t" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$t" +%Y-%m-%dT%H:%M:%SZ; }
con() { aws ec2 get-console-output --instance-id "$1" --latest --query Output --output text 2>/dev/null | tr -d '\r'; }
nats() {
	aws ec2 describe-instances --filters Name=tag:natkit,Values=natkit-module-test Name=instance-state-name,Values=pending,running \
		--query 'Reservations[].Instances[].InstanceId' --output text | tr '\t' '\n'
}
cleanup() {
	set +e
	echo "== destroy" >&2
	tf destroy -auto-approve >"$L/destroy.log" 2>&1 || tf destroy -auto-approve >"$L/destroy.log" 2>&1
	if grep -q 'Destroy complete' "$L/destroy.log"; then
		aws s3 rm "s3://$NATKIT_BUCKET/$KEY" >/dev/null
	else
		tail -n 20 "$L/destroy.log" >&2
	fi
	rm -rf "$L"
}
die() {
	echo "module test: $*" >&2
	if [ -n "${C:-}" ]; then
		echo "-- the client's last probes:" >&2
		con "$C" | LC_ALL=C sed -n 's/.*\(natkit-test: .*\)/\1/p' | tail -n 8 >&2
		for n in $(nats); do
			echo "-- NAT $n:" >&2
			con "$n" | grep -a -E 'natkit' | tail -n 8 >&2
		done
	fi
	exit 1
}
# ready <instance being replaced or -> [failed]: the new NAT instance's ID, once its console says ready. An instance
# whose setup failed ends the wait, unless it's stage 3's leftovers (failed).
ready() {
	for _ in $(seq 80); do
		for n in $(nats); do
			[ "$n" = "$1" ] && continue
			l=$(con "$n" | grep -a 'natkit: ' | tail -n 1)
			case $l in *ready*) echo "$n" && return 0 ;; *FAIL*) [ -n "${2:-}" ] || { echo "$n: $l" >&2 && return 1; } ;; esac
		done
		sleep 15
	done
	return 1
}
# The probes, without what the console put before them (the getty's "login: " prefixes the first):
#   natkit-test: egress|nat64 <n> <epoch> <address|none>
probes() { con "$C" | LC_ALL=C sed -n 's/.*\(natkit-test: .*\)/\1/p' | awk -v n="$1" '$3 + 0 > n + 0'; }
seqno() { probes 0 | awk '$2 == "egress" { n = $3 } END { print n + 0 }'; }
# works <after>: waits for an IPv4 and a NAT64 probe through the EIP after probe <after>
works() {
	for _ in $(seq 40); do
		p=$(probes "$1")
		if echo "$p" | grep -q "egress [0-9]* [0-9]* $EIP\$" && echo "$p" | grep -q "nat64 [0-9]* [0-9]* $EIP\$"; then
			echo "$p" | grep "$EIP" | tail -n 2
			return 0
		fi
		sleep 15
	done
	return 1
}
# gap <after>: the longest time in seconds between two IPv4 probes through the EIP, after probe <after>
gap() {
	probes "$1" | awk -v ip="$EIP" '$2 == "egress" && $5 == ip { if (last && $4 - last > max) max = $4 - last; last = $4 }
		END { print max + 0 }'
}
# refreshed: waits for the group's instance refresh to finish (the old instance is terminated at the end)
refreshed() {
	for _ in $(seq 60); do
		s=$(aws autoscaling describe-instance-refreshes --auto-scaling-group-name "$ASG" --max-records 1 \
			--query 'InstanceRefreshes[0].Status' --output text)
		case $s in Successful) return 0 ;; Failed | Cancelled | RollbackFailed | RollbackSuccessful) echo "refresh: $s" >&2 && return 1 ;; esac
		sleep 15
	done
	return 1
}

C=''
trap cleanup EXIT
tf init -reconfigure -backend-config="bucket=$NATKIT_BUCKET" -backend-config="key=$KEY" -backend-config="region=$AWS_REGION" >"$L/init.log" 2>&1 ||
	{ tail -n 20 "$L/init.log" >&2; die "init"; }

rt=$(aws ec2 describe-route-tables --filters "Name=association.subnet-id,Values=$TF_VAR_private_subnet" --query 'RouteTables[0].RouteTableId' --output text)
for _ in $(seq 60); do
	busy=$(aws ec2 describe-route-tables --route-table-ids "$rt" --query "length(RouteTables[0].Routes[?DestinationCidrBlock=='0.0.0.0/0'])" --output text)
	[ "$busy" = 0 ] && break
	echo "$rt has a default route (another test's?), waiting" >&2
	sleep 30
done
[ "$busy" = 0 ] || die "$rt still has a default route after 30 min: another test, or one that left it behind"

echo "== 1. the defaults"
apply 1
C=$(out client) EIP=$(out eip) ASG=$(out asg)
echo "client $C, EIP $EIP, group $ASG"
n1=$(ready -) || die "NAT not ready"
echo "NAT $n1"
works 0 || die "no egress"
[ "$(aws ec2 describe-security-groups --group-ids "$(out public_sg)" --query 'length(SecurityGroups[0].IpPermissions)' --output text)" = 0 ] ||
	die "the public security group admits something"
vpc=$(aws ec2 describe-subnets --subnet-ids "$TF_VAR_public_subnet" --query 'Subnets[0].VpcId' --output text)
cidr=$(aws ec2 describe-vpcs --vpc-ids "$vpc" --query 'Vpcs[0].CidrBlock' --output text)
[ "$(aws ec2 describe-security-groups --group-ids "$(out private_sg)" --query 'SecurityGroups[0].IpPermissions[0].IpRanges[0].CidrIp' --output text)" = "$cidr" ] ||
	die "the private security group doesn't admit $cidr"

echo "== 2. handover"
s0=$(seqno)
export TF_VAR_keepalive=601
apply 2
n2=$(ready "$n1") || die "the replacement isn't ready"
echo "NAT $n2"
refreshed || die "the instance refresh didn't finish"
works "$(seqno)" || die "no egress after the refresh"
g=$(gap "$s0")
echo "longest time without egress: $g s"
[ "$g" -le "$MAX" ] || die "the handover took $g s, more than $MAX"

echo "== 3. a replacement that fails"
s2=$(seqno)
export TF_VAR_keepalive=soon
apply 3
f=''
for _ in $(seq 40); do
	for n in $(nats); do
		[ "$n" = "$n2" ] || ! con "$n" | grep -a -q 'natkit: FAIL user-data: tcp_keepalive_time' || f=$n
	done
	[ -z "$f" ] || break
	sleep 15
done
[ -n "$f" ] || die "no replacement refused its user-data"
echo "NAT $f failed its setup"
# past the warmup (90 s), after which the group used to terminate the old instance whatever the new one's state
sleep 150
nats | grep -qx "$n2" || die "the group terminated $n2, the instance that routes"
st=$(aws autoscaling describe-auto-scaling-instances --instance-ids "$n2" --query 'AutoScalingInstances[0].LifecycleState' --output text)
[ "$st" = InService ] || die "$n2, the instance that routes, is $st in the group"
# an outage that hasn't ended is no gap between two probes that got through: it must work now
works "$(seqno)" >/dev/null || die "no egress while the replacement fails"
g=$(gap "$s2")
echo "longest time without egress: $g s"
[ "$g" -le 10 ] || die "egress stopped for $g s while the replacement failed"
export TF_VAR_keepalive=602
apply 3b
n2=$(ready "$n2" failed) || die "the replacement isn't ready"
echo "NAT $n2"
refreshed || die "the instance refresh didn't finish"
works "$(seqno)" || die "no egress after the refresh"
g=$(gap "$s2")
[ "$g" -le "$MAX" ] || die "egress stopped for $g s on the way back to a good setting"

echo "== 4. conntrack alarm"
for _ in $(seq 20); do
	d=$(aws cloudwatch get-metric-statistics --namespace natkit --metric-name conntrack_allowance_available \
		--dimensions Name=Name,Value=natkit-module-test --start-time "$(ago 900)" --end-time "$(ago 0)" --period 60 \
		--statistics Minimum --query 'Datapoints[-1].Minimum' --output text)
	[ "$d" = None ] || break
	sleep 15
done
[ "$d" != None ] || die "no conntrack_allowance_available datapoint for natkit-module-test"
# OK, after minutes of reports: INSUFFICIENT_DATA would be an alarm that doesn't watch what natkit publishes
for _ in $(seq 20); do
	a=$(aws cloudwatch describe-alarms --alarm-names "$(out alarm)" --query 'MetricAlarms[0].StateValue' --output text)
	[ "$a" != OK ] || break
	sleep 15
done
echo "allowance available: $d, alarm: $a"
[ "$a" = OK ] || die "the alarm is $a"

echo "== 5. client_cidrs without the client, untracked"
pub=$(aws ec2 describe-subnets --subnet-ids "$TF_VAR_public_subnet" --query 'Subnets[0].CidrBlock' --output text)
export TF_VAR_client_cidrs="[\"$pub\"]" TF_VAR_untracked=true
apply 5
n3=$(ready "$n2") || die "the replacement isn't ready"
echo "NAT $n3"
refreshed || die "the instance refresh didn't finish"
s1=$(seqno)
sleep 45
p=$(probes "$s1")
echo "$p" | tail -n 4
[ "$(echo "$p" | grep -c ' none$')" -ge 6 ] && ! echo "$p" | grep -qv ' none$' || die "a client outside client_cidrs got through"

echo "== 6. the default client_cidrs, untracked, tcp_established_timeout 350"
unset TF_VAR_client_cidrs
export TF_VAR_tcp_established_timeout=350
apply 6
n4=$(ready "$n3") || die "the replacement isn't ready"
echo "NAT $n4"
works "$(seqno)" || die "no egress"
t=$(aws ec2 describe-network-interfaces --filters "Name=attachment.instance-id,Values=$n4" \
	--query 'NetworkInterfaces[].ConnectionTrackingConfiguration.TcpEstablishedTimeout' --output text | tr '\t' ' ')
[ "$t" = '350 350' ] || die "the ENIs' TCP established timeouts are '$t', not 350 and 350"
echo "module test: ok"
