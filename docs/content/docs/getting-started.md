---
title: Getting started
description: Launch the AMI and route a private subnet through it.
weight: 10
icon: rocket_launch
---

natkit is a NAT instance AMI for AWS, built for arm64 and x86_64. It has [fck-nat](https://github.com/AndrewGuenther/fck-nat)'s features on [LinuxKit](https://github.com/linuxkit/linuxkit) instead of Amazon Linux, systemd and Packer. The OS is an immutable in-memory image:

- a minimal kernel from [lk-kernel](https://github.com/ulm0/lk-kernel);
- containerd;
- a handful of service containers.

There's no package manager, SSH or SSM agent.

## Launch

1. Launch the AMI in a public subnet with a public IP. Pick `natkit-<version>-arm64` or `natkit-<version>-amd64` to match the instance type.
2. Turn off source/dest check on its ENI, or let natkit do it with [`eni_id`](../configuration/).
3. Point the private subnets' default route at it, as you would for fck-nat.

The AMI requires IMDSv2.

Without user-data, natkit is a plain NAT out of the primary ENI. For the HA layout, use the [OpenTofu module](../opentofu/).

## Instance size

The CloudWatch agent needs at least a `t4g.micro` or `t3.micro`. Boots are tested on those two types.

{{< alert context="warning" text="`t4g.nano` does not boot yet: the in-memory image does not fit in 512 MB." />}}

## Check that it's up

natkit prints its result as one line on the serial console:

```sh
aws ec2 get-console-output --instance-id i-0123 --latest --query Output --output text | grep natkit:
```

```
natkit: ready, public eth0 private eth1, eip eipalloc-0123, nat64 on, cwagent on
natkit: FAIL <reason>
```
