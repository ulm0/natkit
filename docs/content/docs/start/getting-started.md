---
title: Getting started
description: Get the AMI, pick an instance size, and check that it's up.
weight: 20
aliases: [/docs/getting-started/]
---

natkit runs from its public AMI in us-east-1, or one you import into your own account. Pick an instance size, and deploy it with the [OpenTofu module](/docs/deploy/opentofu/) or [by hand](/docs/deploy/by-hand/).

## Get the AMI

The AMIs are public in **us-east-1**, owned by account `846227790340`: `natkit-<version>-arm64` and `natkit-<version>-amd64`. Each [release](https://github.com/ulm0/natkit/releases) lists their IDs.

Releases up to v0.1.8 were published from account `208689161537`, which is being retired; use v0.1.9 or later, owned by `846227790340`.

They carry three tags every account sees (EC2's [shared tags](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/Using_Tags.html#sharing-tags)):

| Tag | Value |
|---|---|
| `ec2:SharedTag/natkit.io/version` | the release, e.g. `v0.1.9` |
| `ec2:SharedTag/natkit.io/arch` | `arm64` or `amd64` |
| `ec2:SharedTag/natkit.io/commit` | the commit it was built from |

Look them up by tag or by name, always with the owner: anyone can register an AMI with the same name or the same tags.

```sh
aws ec2 describe-images --region us-east-1 --owners 846227790340 \
  --filters Name=tag:ec2:SharedTag/natkit.io/version,Values=v0.1.9 Name=tag:ec2:SharedTag/natkit.io/arch,Values=arm64 \
  --query 'Images[0].ImageId' --output text
```

In OpenTofu:

```hcl
data "aws_ami" "natkit" {
  owners = ["846227790340"]
  filter {
    name   = "tag:ec2:SharedTag/natkit.io/version"
    values = ["v0.1.9"]
  }
  filter {
    name   = "tag:ec2:SharedTag/natkit.io/arch"
    values = ["arm64"]
  }
}
```

There's no `latest` tag. Pin the version: the [module](/docs/deploy/opentofu/) and the AMI go together, from the same release, and a newer AMI would replace the instance at an `apply` nobody planned. To take the newest anyway, drop the version filter and set `most_recent = true`, and upgrade the module with it.

If your account restricts which AMIs it launches ([Allowed AMIs](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-allowed-amis.html)), allow that owner.

A release's AMIs are never removed. Two years after its creation AWS deprecates a public AMI: it still launches, but a lookup needs `include_deprecated = true` to find it (see [Upgrading](/docs/deploy/upgrading/#how-long-an-ami-stays-up)).

### In another Region

Import a release's disk image into your own account and Region, or [build it](/docs/develop/building/):

```sh
V=v0.1.9 ARCH=arm64 # or amd64
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
