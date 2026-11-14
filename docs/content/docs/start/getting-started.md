---
title: Getting started
description: Import the AMI, pick an instance size, and check that it's up.
weight: 20
aliases: [/docs/getting-started/]
---

natkit runs from an AMI in your own account. Import it, pick an instance size, and deploy it with the [OpenTofu module](/docs/deploy/opentofu/) or [by hand](/docs/deploy/by-hand/).

## Get the AMI

The AMIs aren't public. Import a [release](https://github.com/ulm0/natkit/releases)'s disk image into your own account and region, or [build it](/docs/develop/building/):

```sh
V=v0.1.4 ARCH=arm64 # or amd64
curl -fsSLO "https://github.com/ulm0/natkit/releases/download/$V/natkit-$V-$ARCH-efi.img.gz"
gunzip "natkit-$V-$ARCH-efi.img.gz"
AWS_REGION=us-east-1 NATKIT_BUCKET=my-import-bucket scripts/ami.sh "natkit-$V-$ARCH-efi.img" $ARCH "natkit-$V-$ARCH"
```

[`scripts/ami.sh`](https://github.com/ulm0/natkit/blob/main/scripts/ami.sh) uploads the image to the bucket, imports it as a snapshot, registers the AMI and prints its ID. It needs the [`vmimport` service role](https://docs.aws.amazon.com/vm-import/latest/userguide/required-permissions.html).

## Instance size

| Architecture | Minimum |
|---|---|
| arm64 | `t4g.nano` |
| amd64 | `t3.nano`, `t3a.nano` |

0.5 GiB of memory is enough. At boot natkit copies its read-only root, a 66 MB squashfs, from the EBS volume into memory, and doesn't read the volume again. The [CloudWatch agent](/docs/operate/monitoring/) needs 1 GiB (`t4g.micro`, `t3.micro`).

A smaller instance can also have smaller limits: a `t3.nano` tracks 25 000 connections, a `t4g.nano` as many as a `t4g.micro`. See [Performance](/docs/operate/performance/), and [Costs](/docs/operate/costs/) for what each size and everything around it is billed.

## Deploy

With the [OpenTofu module](/docs/deploy/opentofu/), next to the [VPC module](/docs/deploy/vpc-module/) or not, or [by hand](/docs/deploy/by-hand/). Then check that it's up.

## Check that it's up

natkit prints its result as one line on the serial console:

```sh
aws ec2 get-console-output --instance-id i-0123 --latest --query Output --output text | grep natkit:
```

```
natkit: ready, public eth0 private eth1, eip eipalloc-0123, nat64 on, cwagent on
natkit: FAIL <reason>
```
