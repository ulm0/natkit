---
title: Overview
description: What natkit is, and the ways to run it.
weight: 10
---

natkit is a NAT instance AMI for AWS, built for arm64 and x86_64. It has [fck-nat](https://github.com/AndrewGuenther/fck-nat)'s features on [LinuxKit](https://github.com/linuxkit/linuxkit) instead of Amazon Linux, systemd and Packer. The OS is an immutable in-memory image:

- a minimal kernel from [lk-kernel](https://github.com/ulm0/lk-kernel);
- containerd;
- a handful of service containers.

There's no package manager, SSH or SSM agent. [natkit and fck-nat](/docs/start/comparison/) maps one onto the other.

## What it does

- Masquerades the private subnets' IPv4 traffic out of its public interface, and translates `64:ff9b::/96` for IPv6-only subnets ([NAT64](/docs/how-it-works/nat64/)).
- [High availability](/docs/how-it-works/high-availability/): an Auto Scaling group of one, a static ENI the route tables point at, and an EIP. A replacement goes into service only once it has set itself up.
- Publishes EC2's connection-tracking allowance to CloudWatch, without an agent, for an [alarm](/docs/operate/monitoring/#alarms).
- Runs on 0.5 GiB of memory: a `t4g.nano` with its EIP is $6.80 a month in US East ([Costs](/docs/operate/costs/)).

## Ways to run it

1. [Get the AMI](/docs/start/getting-started/) into your account.
2. Deploy it:
   - with the [OpenTofu module](/docs/deploy/opentofu/), which sets up the high-availability layout, its role and the alarm, also [next to the VPC module](/docs/deploy/vpc-module/);
   - or [by hand](/docs/deploy/by-hand/): one instance, or an Auto Scaling group of your own.
3. [Upgrade](/docs/deploy/upgrading/) the module and the AMI together, from the same release.
