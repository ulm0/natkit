package main

import (
	"fmt"
	"strings"
)

const joolPool6 = "64:ff9b::/96"

// ruleset: natkit's nftables ruleset, loaded in one transaction (nft -f -), so there's no moment without the rules.
//
// Masquerade out of the public interface: TCP and UDP to ports 1024-61000, because Jool's NAT64 sessions use
// 61001-65535 of the same address and masquerade otherwise keeps a client's source port, which can be one of those.
//
// The instance itself accepts nothing new (it has no listening service; this keeps it so): replies, ICMP (path MTU,
// IPv6 neighbour discovery) and DHCP only. Forwarded: packets conntrack can't place in a connection are dropped
// (they'd skip the masquerade and leave with a private source), and, with client_cidrs, new connections from
// anywhere else. Replies to a forwarded connection always pass. NAT64 is gated in prerouting, at raw priority: Jool
// takes the client's IPv6 packet at prerouting priority -75 and sends the translated IPv4 one itself, so neither
// reaches the forward chain. auto-merge: overlapping CIDRs would otherwise be a conflicting-intervals error.
func ruleset(public string, u *userData) string {
	policy, sets, fwd, pre := "accept", "", "", ""
	if u.clients {
		policy = "drop"
		e4, e6 := "", ""
		if len(u.clients4) > 0 {
			e4 = "elements = { " + strings.Join(u.clients4, ", ") + " }"
		}
		if len(u.clients6) > 0 {
			e6 = "elements = { " + strings.Join(u.clients6, ", ") + " }"
		}
		sets = fmt.Sprintf(`set clients4 { type ipv4_addr; flags interval; auto-merge; %s }
	set clients6 { type ipv6_addr; flags interval; auto-merge; %s }`, e4, e6)
		fwd = `ct state established,related accept
		ip saddr @clients4 accept
		ip6 saddr @clients6 accept`
		pre = fmt.Sprintf(`chain prerouting {
		type filter hook prerouting priority raw; policy accept;
		ip6 daddr %s ip6 saddr != @clients6 drop
	}`, joolPool6)
	}
	return fmt.Sprintf(`flush ruleset
table ip natkit {
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		oifname "%[1]s" meta l4proto { tcp, udp } masquerade to :1024-61000 comment "NAT routing rule installed by natkit"
		oifname "%[1]s" masquerade
	}
}
table inet natkit {
	%[2]s
	chain input {
		type filter hook input priority filter; policy drop;
		ct state established,related accept
		iifname "lo" accept
		meta l4proto icmp accept
		meta l4proto ipv6-icmp accept
		udp dport 68 accept
		udp dport 546 accept
	}
	chain forward {
		type filter hook forward priority filter; policy %[3]s;
		ct state invalid drop
		%[4]s
	}
	%[5]s
}
`, public, sets, policy, fwd, pre)
}
