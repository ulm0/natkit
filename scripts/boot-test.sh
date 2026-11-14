#!/bin/sh
# Boot test of a natkit AMI in fck-nat's HA setup, end to end:
#  1. an EIP and a static ENI (source/dest check off) are made; the private subnet's route table gets
#     0.0.0.0/0 -> the ENI; an SSM parameter holds a CloudWatch agent config
#  2. NAT instance A boots with user-data eip_id + eni_id + client_cidrs (the private subnet) + tuning + cwagent +
#     nat64: false: its console line must say ready, private eth1, the EIP, nat64 off, cwagent on. (Without Jool:
#     v0.1.0 only forwarded IPv4 with it on.)
#  3. a client (Amazon Linux 2023, private subnet, no public IP) prints its egress IP on its console every 15 s:
#     it must be the EIP
#  4. handover: instance B boots with the same user-data but NAT64 (the default) while A holds the ENI: B must take
#     the ENI over and the EIP; A is then terminated, and the client's egress must work through B
#  5. the CloudWatch agent on B (pulled at boot) must publish mem_used_percent
# When the subnets have IPv6 CIDRs, NAT64 too: the route table gets 64:ff9b::/96 -> the ENI and the client reaches an
# IPv4-only address through it over IPv6 (step 4, through B); the egress must again be the EIP.
# Everything is tagged natkit.io/boot-test=<tag> and removed on exit (CI's boot-cleanup also removes it by tag).
#
#   scripts/boot-test.sh <ami-id> <arm64|amd64>
#
# Needs AWS credentials + AWS_REGION and:
#   NATKIT_TEST_PUBLIC_SUBNET        subnet with an internet gateway route (the NAT instances)
#   NATKIT_TEST_PRIVATE_SUBNET       subnet with its own route table and no other egress (the client), same VPC/AZ;
#                                    no IPv6 egress either (no egress-only gateway), or the NAT64 check proves nothing
#   NATKIT_TEST_INSTANCE_PROFILE     NAT role: ec2:AssociateAddress, ec2:AttachNetworkInterface,
#                                    ec2:DetachNetworkInterface, ec2:DescribeNetworkInterfaces,
#                                    ec2:ModifyNetworkInterfaceAttribute, ssm:GetParameter, CloudWatchAgentServerPolicy
# Both subnets' instances use the VPC's default security group (admits traffic from itself).
# Runs on Linux (CI) or macOS: e.g. against the AMIs of a `build` run dispatched with boot_test off, with
# NATKIT_BOOT_TEST_TAG set to something unique. One at a time: every run routes the private subnet through its ENI.
set -eu
AMI=$1 ARCH=$2
TAG=${NATKIT_BOOT_TEST_TAG:-$AMI}
PUB=${NATKIT_TEST_PUBLIC_SUBNET:?} PRIV=${NATKIT_TEST_PRIVATE_SUBNET:?} PROFILE=${NATKIT_TEST_INSTANCE_PROFILE:?}
case $ARCH in
# micro, not nano, for the NAT instances: the CloudWatch agent (README) needs the memory
arm64) TYPE=t4g.micro CTYPE=t4g.micro ;;
amd64) TYPE=t3.micro CTYPE=t3.micro ;;
*) echo "arch must be arm64 or amd64" >&2 && exit 1 ;;
esac
PARAM=/natkit/boot-test/$TAG/cwagent
NS=natkit-boot-test
TAGS="{Key=natkit.io/boot-test,Value=$TAG}"
eip='' eni='' a='' b='' client='' rt='' v6='' ud=$(mktemp) cud=$(mktemp)

cleanup() {
	set +e
	# shellcheck disable=SC2086 # word splitting on purpose: the IDs set so far
	ids=$(echo $a $b $client)
	if [ -n "$ids" ]; then
		# shellcheck disable=SC2086
		aws ec2 terminate-instances --instance-ids $ids >/dev/null
		# shellcheck disable=SC2086
		aws ec2 wait instance-terminated --instance-ids $ids
	fi
	[ -z "$rt" ] || aws ec2 delete-route --route-table-id "$rt" --destination-cidr-block 0.0.0.0/0
	[ -z "$rt" ] || [ -z "$v6" ] || aws ec2 delete-route --route-table-id "$rt" --destination-ipv6-cidr-block 64:ff9b::/96
	[ -z "$eni" ] || aws ec2 delete-network-interface --network-interface-id "$eni"
	[ -z "$eip" ] || aws ec2 release-address --allocation-id "$eip"
	aws ssm delete-parameter --name "$PARAM" 2>/dev/null
	rm -f "$ud" "$cud"
}
trap cleanup EXIT
die() { echo "boot test: $*" >&2; exit 1; }
# ago <seconds>: that long ago in UTC, ISO 8601; GNU date (CI) or BSD date (macOS, to run the test locally)
ago() { t=$(($(date +%s) - $1)); date -u -d "@$t" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$t" +%Y-%m-%dT%H:%M:%SZ; }

# console <instance>: its serial console output; line <instance> <pattern>: the last matching line, waiting up to 15 min
console() { aws ec2 get-console-output --instance-id "$1" --latest --query Output --output text 2>/dev/null; }
line() {
	for _ in $(seq 60); do
		sleep 15
		l=$(console "$1" | tr -d '\r' | grep -a -e "$2" | tail -n 1) && [ -n "$l" ] && echo "$l" && return
	done
	console "$1" | tail -n 40 >&2
	return 1
}

# NAT64 needs IPv6 in both subnets
v6=$(aws ec2 describe-subnets --subnet-ids "$PUB" "$PRIV" --query 'length(Subnets[?Ipv6CidrBlockAssociationSet[0].Ipv6CidrBlock != null])' --output text)
[ "$v6" = 2 ] && v6=1 || v6=''
# client_cidrs: the private subnet's CIDRs, so the client is the one source natkit forwards for
clients=$(aws ec2 describe-subnets --subnet-ids "$PRIV" --output text \
	--query 'Subnets[0].[CidrBlock, Ipv6CidrBlockAssociationSet[0].Ipv6CidrBlock]' | tr '\t' '\n' | grep -v '^None$' | sed 's/.*/"&"/' | paste -sd, -)
[ -n "$v6" ] || echo "no IPv6 in the test subnets: NAT64 is only checked for 'nat64 on'" >&2

# 1. EIP, static ENI, route, agent config
eip=$(aws ec2 allocate-address --domain vpc --tag-specifications "ResourceType=elastic-ip,Tags=[$TAGS]" --query AllocationId --output text)
eip_ip=$(aws ec2 describe-addresses --allocation-ids "$eip" --query 'Addresses[0].PublicIp' --output text)
eni=$(aws ec2 create-network-interface --subnet-id "$PUB" --description "natkit boot test $TAG" ${v6:+--ipv6-address-count 1} \
	--tag-specifications "ResourceType=network-interface,Tags=[$TAGS]" --query NetworkInterface.NetworkInterfaceId --output text)
aws ec2 modify-network-interface-attribute --network-interface-id "$eni" --no-source-dest-check
rt=$(aws ec2 describe-route-tables --filters "Name=association.subnet-id,Values=$PRIV" --query 'RouteTables[0].RouteTableId' --output text)
[ "$rt" != None ] || { rt=; die "$PRIV has no route table of its own"; }
aws ec2 create-route --route-table-id "$rt" --destination-cidr-block 0.0.0.0/0 --network-interface-id "$eni" >/dev/null ||
	aws ec2 replace-route --route-table-id "$rt" --destination-cidr-block 0.0.0.0/0 --network-interface-id "$eni"
[ -z "$v6" ] || aws ec2 create-route --route-table-id "$rt" --destination-ipv6-cidr-block 64:ff9b::/96 --network-interface-id "$eni" >/dev/null ||
	aws ec2 replace-route --route-table-id "$rt" --destination-ipv6-cidr-block 64:ff9b::/96 --network-interface-id "$eni"
aws ssm put-parameter --name "$PARAM" --type String --overwrite --value \
	"{\"metrics\":{\"namespace\":\"$NS\",\"append_dimensions\":{\"InstanceId\":\"\${aws:InstanceId}\"},\"metrics_collected\":{\"mem\":{\"measurement\":[\"mem_used_percent\"],\"metrics_collection_interval\":60}}}}" >/dev/null
echo "EIP $eip ($eip_ip), ENI $eni, route table $rt" >&2

cat >"$ud" <<EOF
eip_id: $eip
eni_id: $eni
client_cidrs: [$clients]
nf_conntrack_max: 131072
ip_local_port_range: "1024 61000"
cwagent_enabled: true
cwagent_cfg_param_name: $PARAM
EOF
nat() { # nat <Name>: launches a NAT instance, prints its ID
	aws ec2 run-instances --image-id "$AMI" --instance-type "$TYPE" --iam-instance-profile "Name=$PROFILE" \
		--network-interfaces "DeviceIndex=0,SubnetId=$PUB,AssociatePublicIpAddress=true,DeleteOnTermination=true${v6:+,Ipv6AddressCount=1}" \
		--metadata-options HttpTokens=required,HttpEndpoint=enabled,HttpPutResponseHopLimit=1 \
		--user-data "file://$ud" --block-device-mappings 'DeviceName=/dev/xvda,Ebs={Encrypted=true}' \
		--tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$1},$TAGS]" \
		--query 'Instances[0].InstanceId' --output text
}
want="natkit: ready, public eth0 private eth1, eip $eip, nat64 on, cwagent on"

# 2. NAT instance A, without NAT64: plain IPv4 forwarding must not depend on Jool
echo "nat64: false" >>"$ud"
a=$(nat natkit-boot-test-a)
sed -i.bak '$d' "$ud" && rm -f "$ud.bak" # B gets the default: NAT64 on
echo "NAT A $a" >&2
l=$(line "$a" 'natkit: ') || die "A: no natkit line on the console after 15 min"
echo "A: $l"
wa=$(echo "$want" | sed 's/nat64 on/nat64 off/')
[ "$l" = "$wa" ] || die "A: want '$wa'"
# the tuning took, and the allowance line shows it
console "$a" | tr -d '\r' | grep -a -q 'natkit-init: EC2 conntrack allowance [0-9]*, nf_conntrack_max 131072$' ||
	die "A: no 'EC2 conntrack allowance ..., nf_conntrack_max 131072' on the console"

# 3. the client
case $ARCH in arm64) p=arm64 ;; amd64) p=x86_64 ;; esac
cami=$(aws ssm get-parameter --name "/aws/service/ami-amazon-linux-latest/al2023-ami-minimal-kernel-default-$p" --query Parameter.Value --output text)
# detached from cloud-init, which would otherwise wait for (or reap) the loop
cat >"$cud" <<'EOF'
#!/bin/sh
H=checkip.amazonaws.com
setsid sh -c 'n=0; while :; do n=$((n + 1))
	echo "natkit-test: egress $n $(curl -s -m 5 https://'$H' || echo none)" >/dev/ttyS0
	a=$(getent ahostsv4 '$H' | cut -d" " -f1 | head -n 1)
	echo "natkit-test: nat64 $n $(curl -s -m 5 --resolve "'$H':443:[64:ff9b::$a]" https://'$H' || echo none)" >/dev/ttyS0
	sleep 15; done' </dev/null >/dev/null 2>&1 &
EOF
client=$(aws ec2 run-instances --image-id "$cami" --instance-type "$CTYPE" \
	--network-interfaces "DeviceIndex=0,SubnetId=$PRIV,AssociatePublicIpAddress=false,DeleteOnTermination=true${v6:+,Ipv6AddressCount=1}" \
	--metadata-options HttpTokens=required,HttpEndpoint=enabled --user-data "file://$cud" \
	--tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=natkit-boot-test-client},$TAGS]" \
	--query 'Instances[0].InstanceId' --output text)
echo "client $client" >&2
l=$(line "$client" "natkit-test: egress [0-9]* $eip_ip\$") || die "client: no egress through the EIP after 15 min"
echo "client: $l"

# 4. handover: B takes the ENI over from A, which is still running
b=$(nat natkit-boot-test-b)
echo "NAT B $b" >&2
l=$(line "$b" 'natkit: ') || die "B: no natkit line on the console after 15 min"
echo "B: $l"
[ "$l" = "$want" ] || die "B: want '$want'"
h=$(aws ec2 describe-network-interfaces --network-interface-ids "$eni" --query 'NetworkInterfaces[0].Attachment.InstanceId' --output text)
[ "$h" = "$b" ] || die "the ENI is on $h, not on B ($b)"
aws ec2 terminate-instances --instance-ids "$a" >/dev/null
aws ec2 wait instance-terminated --instance-ids "$a"
a=
n0=$(console "$client" | tr -d '\r' | grep -a 'natkit-test: egress' | tail -n 1 | cut -d' ' -f3)
for _ in $(seq 40); do
	sleep 15
	# shellcheck disable=SC2046 # split on purpose: natkit-test: egress <n> <ip>
	set -- $(console "$client" | tr -d '\r' | grep -a 'natkit-test: egress' | tail -n 1)
	[ "${3:-0}" -gt "${n0:-0}" ] && [ "${4:-}" = "$eip_ip" ] && break
	set --
done
[ $# -gt 0 ] || die "client: no egress through B after 10 min"
echo "client after the handover: $*"
if [ -n "$v6" ]; then
	for _ in $(seq 40); do
		sleep 15
		# shellcheck disable=SC2046 # split on purpose: natkit-test: nat64 <n> <ip>
		set -- $(console "$client" | tr -d '\r' | grep -a 'natkit-test: nat64' | tail -n 1)
		[ "${3:-0}" -gt "${n0:-0}" ] && [ "${4:-}" = "$eip_ip" ] && break
		set --
	done
	[ $# -gt 0 ] || die "client: no NAT64 egress through B after 10 min"
	echo "client after the handover: $*"
fi

# 5. the CloudWatch agent on B
for _ in $(seq 40); do
	sleep 15
	c=$(aws cloudwatch get-metric-statistics --namespace "$NS" --metric-name mem_used_percent \
		--dimensions "Name=InstanceId,Value=$b" --statistics Maximum --period 60 \
		--start-time "$(ago 900)" --end-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		--query 'length(Datapoints)' --output text)
	[ "$c" -gt 0 ] && echo "B: CloudWatch agent published $c datapoint(s)" && break
	c=0
done
[ "$c" -gt 0 ] || { console "$b" | tr -d '\r' | grep -a 'natkit-cwagent' | tail -n 20 >&2; die "B: no CloudWatch agent metrics after 10 min"; }
echo "boot test: ok" >&2
