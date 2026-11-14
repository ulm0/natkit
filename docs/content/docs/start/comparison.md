---
title: natkit and fck-nat
description: How natkit maps fck-nat's pieces.
weight: 30
aliases: [/docs/comparison/]
---

| fck-nat | natkit |
|---|---|
| Amazon Linux 2023, systemd unit `fck-nat.service` | LinuxKit `onboot` container `natkit-init` |
| `/etc/fck-nat.conf` written by a cloud-init script | YAML user-data with the same keys |
| `aws` CLI | `curl --aws-sigv4` with the instance role |
| `iptables -t nat ... MASQUERADE` | one nftables ruleset, applied atomically |
| Jool 4.1.15 built with DKMS, only in the separate `nat64-` AMIs | Jool 4.1.15 modules built against natkit's kernel, in every AMI |
| CloudWatch agent installed | CloudWatch agent pulled by digest at boot, only when enabled |
| SSH / SSM | EC2 Serial Console (root password hash from SSM, optional) |

`natkit-init` follows fck-nat's `service/fck-nat.sh` (MIT, Andrew Guenther).
