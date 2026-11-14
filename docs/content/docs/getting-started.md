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

## Get the AMI

The AMIs aren't public. Import a [release](https://github.com/ulm0/natkit/releases)'s disk image into your own account and region, or [build it](../building/):

```sh
V=v0.1.1 ARCH=arm64 # or amd64
curl -fsSLO "https://github.com/ulm0/natkit/releases/download/$V/natkit-$V-$ARCH-efi.img.gz"
gunzip "natkit-$V-$ARCH-efi.img.gz"
AWS_REGION=us-east-1 NATKIT_BUCKET=my-import-bucket scripts/ami.sh "natkit-$V-$ARCH-efi.img" $ARCH "natkit-$V-$ARCH"
```

[`scripts/ami.sh`](https://github.com/ulm0/natkit/blob/main/scripts/ami.sh) uploads the image to the bucket, imports it as a snapshot, registers the AMI and prints its ID. It needs the [`vmimport` service role](https://docs.aws.amazon.com/vm-import/latest/userguide/required-permissions.html).

## Launch

1. Launch the AMI in a public subnet with a public IP. Pick `natkit-<version>-arm64` or `natkit-<version>-amd64` to match the instance type.
2. Turn off source/dest check on its ENI, or let natkit do it with [`eni_id`](../configuration/).
3. Point the private subnets' default route at it, as you would for fck-nat.

The AMI requires IMDSv2.

Without user-data, natkit is a plain NAT out of the primary ENI. For the HA layout, use the [OpenTofu module](../opentofu/).

## Instance size

| Architecture | Minimum |
|---|---|
| arm64 | `t4g.nano` |
| amd64 | `t3.nano`, `t3a.nano` |

0.5 GiB of memory is enough: the root filesystem is a read-only partition on the EBS volume, not in memory. The [CloudWatch agent](../cloudwatch/) needs 1 GiB (`t4g.micro`, `t3.micro`).

A smaller instance also has smaller limits. See [Performance](../performance/).

## Check that it's up

natkit prints its result as one line on the serial console:

```sh
aws ec2 get-console-output --instance-id i-0123 --latest --query Output --output text | grep natkit:
```

```
natkit: ready, public eth0 private eth1, eip eipalloc-0123, nat64 on, cwagent on
natkit: FAIL <reason>
```
