---
title: Performance
description: What a load test measured, and what limits a NAT instance.
weight: 75
icon: speed
---

A load test in `us-east-1` put a client in a private subnet, natkit in between, and a server reached by its public IP. Client and server were `c7g.xlarge`; without the NAT they exchanged 12.4 Gbit/s and 137 000 new connections per second.

## Throughput

`iperf3` over TCP for 20 seconds:

| Through natkit on | 1 stream | 8 streams, up | 8 streams, down |
|---|---|---|---|
| `t4g.micro` | 1.6 to 2.4 Gbit/s | 2.0 to 3.4 Gbit/s | 1.6 to 2.3 Gbit/s |
| `c7gn.medium` | 4.8 Gbit/s | 4.8 Gbit/s | 4.8 Gbit/s |

The `t4g.micro` figures are burst rates. Its baseline bandwidth is far lower, and a sustained transfer falls back to it once the network credits run out.

## New connections

`ab` opened a new connection for every request, to 8 server ports at once, through natkit on a `t4g.micro`:

| Burst | Static ENI tracked (default) | Static ENI untracked |
|---|---|---|
| 80 000 connections | 20 000 to 23 000 per second, none failed | |
| 100 000 connections | stalls: EC2 drops packets | 24 000 per second, none failed |
| 200 000 connections | stalls; 0.2% failed, 1 100 per second on average | |

During the stalls the instance was not busy: at least 83% of its CPU was idle and a quarter of its memory was in use. The limit was EC2's.

## What limits a NAT instance

**EC2's connection tracking.** EC2 tracks the connections that a security group admits, up to a number per instance that depends on the instance type. Past it, EC2 drops packets before they reach the instance. natkit prints the number at boot, before any traffic:

```
natkit-init: EC2 conntrack allowance 153911, nf_conntrack_max 119040
```

| Instance type | Tracked connections allowed | Flows through the NAT, tracked | Untracked |
|---|---|---|---|
| `t3.nano`, `t3a.nano` | 25 000 | 12 500 | 25 000 |
| `t3.micro` | about 51 000 | about 25 000 | about 51 000 |
| `t4g.nano`, `t4g.micro` | about 154 000 | about 77 000 | about 154 000 |
| `c7gn.medium` | about 103 000 | about 51 000 | about 103 000 |

A flow through a NAT instance counts twice, once on each network interface, unless the static ENI is untracked. A closed connection frees its entries after a minute or two, so the limit applies to flows opened within that window. AWS doesn't publish the allowance: read it from the console line or the counters below.

**The kernel's connection tracking.** Linux sizes its own table for a host: about 7 000 flows under 1 GiB of memory. natkit raises the default to one flow per 8 kB of memory: about 118 000 on 1 GiB, about 55 000 on 0.5 GiB. Set [`nf_conntrack_max`](../configuration/) to change it. A full table logs `nf_conntrack: table full, dropping packet` on the serial console.

## Get more out of the allowance

**Untrack the static ENI.** EC2 doesn't track a TCP or UDP flow whose security group admits all traffic in both directions (`0.0.0.0/0`, or `::/0` for IPv6). With the [OpenTofu module](../opentofu/)'s `untracked = true`, the static ENI's security group does that, and each flow through the NAT counts once, on the public ENI. In the test above, a burst of 100 000 connections used about 90 000 of the `t4g.micro`'s allowance instead of exhausting it.

- The security group then admits everyone on the static ENI. natkit's [`client_cidrs`](../configuration/), which the module always sets, is what keeps other sources from using the NAT.
- ICMP is always tracked.
- Connections that come through an NLB, a NAT gateway, PrivateLink, Global Accelerator, a Network Firewall endpoint or an egress-only internet gateway are tracked regardless.
- Turning `untracked` off, or changing the rule, cuts the untracked flows at once: EC2 has no state to keep them.
- The public ENI stays tracked: it admits nothing new, so only replies to the NAT's own connections come in.

**Shorten EC2's idle timeout.** EC2 keeps an idle established TCP connection tracked for 5 days by default, 350 seconds on Nitro v6 instance types. Connections that are abandoned without a FIN or RST hold the allowance that long. The module's `tcp_established_timeout = 350` matches NAT Gateway. Connections idle for longer are then cut, unless they send keepalives.

**Use a larger instance type**, which has a larger allowance.

## Watch the limit

With the [OpenTofu module](../opentofu/), `conntrack_alarm_actions` sets this up: natkit publishes the counters and a CloudWatch alarm goes off when EC2 drops packets, or when the NAT stops reporting. No agent is involved, so it works on a 0.5 GiB instance.

```hcl
conntrack_alarm_actions = [aws_sns_topic.alerts.arn]
```

Without the module, set [`metrics`](../cloudwatch/#allowance-metrics) in the user-data and alarm on the sum of `conntrack_allowance_exceeded`. For the other allowances (packets per second, bandwidth), the [CloudWatch agent](../cloudwatch/#the-cloudwatch-agent) can publish the ENA driver's counters. Add this to its config:

```json
{
  "metrics": {
    "append_dimensions": { "InstanceId": "${aws:InstanceId}" },
    "metrics_collected": {
      "ethtool": {
        "interface_include": ["eth0", "eth1"],
        "metrics_include": [
          "conntrack_allowance_available",
          "conntrack_allowance_exceeded",
          "pps_allowance_exceeded",
          "bw_in_allowance_exceeded",
          "bw_out_allowance_exceeded"
        ]
      }
    }
  }
}
```

- `conntrack_allowance_available` is how many more connections EC2 will track. Alarm when it gets low.
- `conntrack_allowance_exceeded` counts the packets EC2 dropped because the allowance ran out. It should stay flat.
