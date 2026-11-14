---
title: NAT64
description: Jool translates 64:ff9b::/96 for IPv6-only subnets.
weight: 40
icon: swap_horiz
---

NAT64 is on unless the user-data sets `nat64: false`. [Jool](https://nicmx.github.io/Jool/) translates `64:ff9b::/96` to IPv4.

To use it:

- turn on Route 53 Resolver DNS64 on the IPv6 subnets;
- add a `64:ff9b::/96` route to the instance;
- give the instance's ENI an IPv6 address.

With [`client_cidrs`](../configuration/), only its IPv6 sources can use NAT64, and Jool's address-dependent filtering admits only replies from the IPv4 hosts a client contacted. Without it, Jool lets any IPv4 host reach a client's mapping.

Jool's default IPv4 pool is ports 61001-65535 of the instance's address. Keep `ip_local_port_range` below that.

The Jool modules are built against natkit's kernel (Jool 4.1.15).
