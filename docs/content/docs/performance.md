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

`ab` opened a new connection for every request, to 8 server ports at once:

| Through natkit on `t4g.micro` | Result |
|---|---|
| 80 000 connections | 20 000 to 23 000 per second, none failed |
| 200 000 connections | stalls; 0.2% failed, 1 100 per second on average |
| 400 000 connections | stalls; 0.7% failed, 1 100 per second on average |

During the larger bursts the instance was not busy: at least 83% of its CPU was idle and a quarter of its memory was in use. The limit was EC2's.

## What limits a NAT instance

**EC2's connection tracking.** EC2 tracks every connection that a security group admits, up to a number that depends on the instance type. Past it, EC2 drops packets before they reach the instance. A flow through a NAT instance counts twice, once on each network interface:

| Instance type | Tracked connections allowed | Flows through the NAT |
|---|---|---|
| `t4g.micro` | about 154 000 | about 77 000 |
| `c7gn.medium` | about 103 000 | about 51 000 |

A closed connection frees its entries after a minute or two, so the limit applies to flows opened within that window. The allowance varies by instance type and AWS doesn't publish it: measure yours with the counters below.

**The kernel's connection tracking.** Linux sizes its own table for a host: about 7 000 flows under 1 GiB of memory. natkit raises the default to one flow per 8 kB of memory, about 117 000 on 1 GiB. Set [`nf_conntrack_max`](../configuration/) to change it. A full table logs `nf_conntrack: table full, dropping packet` on the serial console.

## Watch the limit

The [CloudWatch agent](../cloudwatch/) can publish the ENA driver's counters. Add this to its config:

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
