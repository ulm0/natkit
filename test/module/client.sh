#!/bin/sh
# The module test's client (user-data): every 2 s, its IPv4 and NAT64 egress address, or none, on the serial console:
#   natkit-test: egress <n> <epoch seconds> <address|none>
#   natkit-test: nat64 <n> <epoch seconds> <address|none>
# A transient systemd unit, not a background job of cloud-init's: that one gets stopped with cloud-init.
cat >/usr/local/bin/natkit-probe <<'P'
#!/bin/sh
H=checkip.amazonaws.com
n=0
while :; do
	n=$((n + 1))
	echo "natkit-test: egress $n $(date +%s) $(curl -s -m 2 https://$H || echo none)" >/dev/ttyS0
	a=$(getent ahostsv4 $H | cut -d" " -f1 | head -n 1)
	echo "natkit-test: nat64 $n $(date +%s) $(curl -s -m 2 --resolve "$H:443:[64:ff9b::$a]" https://$H || echo none)" >/dev/ttyS0
	sleep 2
done
P
chmod +x /usr/local/bin/natkit-probe
systemd-run --unit natkit-probe /usr/local/bin/natkit-probe
