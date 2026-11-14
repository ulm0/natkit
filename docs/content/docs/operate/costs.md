---
title: Costs
description: Everything a natkit deployment is billed for, and the same traffic through a NAT gateway.
weight: 30
aliases: [/docs/costs/]
---

natkit is free. AWS bills what it runs on.

The prices here are AWS's on-demand prices for US East (N. Virginia) in November 2026, in US dollars, before taxes, free tiers and discounts. A month is 730 hours. Other Regions differ: check the pricing pages for [EC2](https://aws.amazon.com/ec2/pricing/on-demand/) (instances, CPU credits, data transfer), [EBS](https://aws.amazon.com/ebs/pricing/), [VPC](https://aws.amazon.com/vpc/pricing/) (public IPv4 addresses, NAT gateway) and [CloudWatch](https://aws.amazon.com/cloudwatch/pricing/).

## A month

One NAT from the [OpenTofu module](/docs/deploy/opentofu/), before any traffic. The module's default instance type is `t4g.micro`; the other columns need `instance_type`.

| | `t4g.micro` (default) | `t4g.nano` | `t3.nano` |
|---|---|---|---|
| Instance | $6.13 | $3.07 | $3.80 |
| Root volume | $0.08 | $0.08 | $0.08 |
| Public IPv4 address (the EIP) | $3.65 | $3.65 | $3.65 |
| **Total** | **$9.86** | **$6.80** | **$7.53** |
| **With the allowance metrics and alarm** (`conntrack_alarm_actions`, $1.22) | **$11.08** | **$8.02** | **$8.75** |

Add [data transfer](#data-transfer) for what the NAT sends to the internet, and [surplus CPU credits](#cpu-credits) if the instance stays busy.

## What is billed

| What | Billed | Price | A month |
|---|---|---|---|
| The instance | while it runs, by the second (60 seconds minimum) | see below | $3.07 and up |
| Its root volume, 1 GiB of gp3 | while the instance exists, running or stopped | $0.08 per GB-month | $0.08 |
| One public IPv4 address | every hour, attached to an instance or not | $0.005 an hour | $3.65 |
| Surplus CPU credits | when the CPU stays above the baseline, and when a busy instance is terminated | $0.04 per vCPU-hour on T4g, $0.05 on T3 and T3a | $0 if it doesn't |
| Data transfer out to the internet | per GB the NAT sends out | $0.09 per GB | by traffic |
| Data transfer between Availability Zones | per GB, when a client is in another zone than the NAT | $0.01 per GB at each end | by traffic |
| Allowance metrics and alarm (optional) | while they are published | 2 metrics, 1 alarm, the requests | $1.22 |
| CloudWatch agent (optional) | per metric it publishes | $0.30 per metric | by its config |
| The AMI's snapshot | while the snapshot exists | $0.05 per GB-month | about $0.005 |

| Instance type | An hour | A month |
|---|---|---|
| `t4g.nano` | $0.0042 | $3.07 |
| `t4g.micro` | $0.0084 | $6.13 |
| `t4g.small` | $0.0168 | $12.26 |
| `t3a.nano` | $0.0047 | $3.43 |
| `t3.nano` | $0.0052 | $3.80 |
| `t3a.micro` | $0.0094 | $6.86 |
| `t3.micro` | $0.0104 | $7.59 |

Not billed: the static ENI, the security groups, the launch template, the Auto Scaling group and its lifecycle hook, the IAM role, the routes, standard SSM parameters, IPv6 addresses and the serial console. Two things that depend on your account:

- **Encryption keys.** The module encrypts the volume with the account's default EBS key. The AWS managed key has no fee; a customer managed key costs $1 a month. The same goes for the key of a `SecureString` parameter that natkit reads.
- **Advanced SSM parameters.** A CloudWatch agent config over 4 KB needs one: $0.05 a month.

## The root volume

At boot natkit copies its root filesystem from the EBS volume into memory, and doesn't read the volume again. The volume still exists for as long as the instance does, and AWS bills it: 1 GiB of gp3, $0.08 a month.

- It can't be smaller. 1 GiB is the minimum size of a gp3 volume.
- It can't be removed. These instance types boot only from EBS, and a root volume can't be detached from a running instance.
- gp3 includes 3 000 IOPS and 125 MiB/s. natkit doesn't pay for more.
- It is billed while the instance is stopped too. It is deleted when the instance is terminated.

## The public IPv4 address

AWS charges $0.005 an hour for every public IPv4 address it provides, in use or idle. That is $3.65 a month: more than a `t4g.nano`.

- With `eip = true`, the default, the EIP is billed from the moment it is allocated, whether or not an instance holds it.
- When natkit associates the EIP with its primary ENI, EC2 releases the address the instance launched with. One address is billed, not two.
- With `eip = false` the instance keeps the address it launched with, at the same price. Turning the EIP off saves nothing.
- Between launch and the association both addresses are billed, by the second.

## CPU credits

T4g, T3 and T3a are burstable instance types. Each has a CPU baseline, and each launches in `unlimited` mode by default; the module doesn't change that. Above the baseline the instance keeps its full speed and spends surplus credits. AWS bills surplus credits in two cases, described in [its documentation](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/burstable-performance-instances-unlimited-mode-concepts.html):

- the instance has spent more than it can earn back in 24 hours;
- the instance is stopped or terminated while it still owes some. A replacement terminates the old instance, so whatever it owes is billed then: at most $0.10 on a `t4g.nano`, $0.19 on a `t4g.micro`, $0.12 on a `t3.nano` and $0.24 on a `t3.micro`.

A NAT forwards packets in the kernel, and that CPU time counts like any other. Sustained traffic means sustained CPU.

| Instance type | Baseline, per vCPU | Averaging 25% of both vCPUs all month | Both vCPUs at 100% all month |
|---|---|---|---|
| `t4g.nano` | 5% | 2 × 0.20 × 730 × $0.04 = $11.68 | 2 × 0.95 × 730 × $0.04 = $55.48 |
| `t4g.micro` | 10% | 2 × 0.15 × 730 × $0.04 = $8.76 | 2 × 0.90 × 730 × $0.04 = $52.56 |
| `t3.nano`, `t3a.nano` | 5% | 2 × 0.20 × 730 × $0.05 = $14.60 | 2 × 0.95 × 730 × $0.05 = $69.35 |
| `t3.micro`, `t3a.micro` | 10% | 2 × 0.15 × 730 × $0.05 = $10.95 | 2 × 0.90 × 730 × $0.05 = $65.70 |

These amounts come on top of the instance price. The last column is the most an instance can cost in surplus credits.

How much traffic keeps an instance above its baseline hasn't been measured. Watch it instead: EC2 publishes `CPUSurplusCreditsCharged` for the instance, at no charge. It should stay at zero.

- A larger instance doesn't make the CPU cheaper on T4g and T3: the next size up doubles the baseline for about the price of the surplus credits it saves.
- To cap the cost, use `standard` mode: an instance that runs out of credits is held to its baseline, so the NAT slows down instead of costing more. natkit hasn't been tested in that mode, and the module has no input for it. For a running instance: `aws ec2 modify-instance-credit-specification --instance-credit-specification "InstanceId=<id>,CpuCredits=standard"`. Its replacement launches in `unlimited` mode again, unless the account's default for the instance family is changed with `aws ec2 modify-default-credit-specification`, which applies to every new instance of that family in the Region.

## Data transfer

natkit adds no charge per GB of its own. AWS's data transfer prices apply to its traffic as to any instance's:

| Traffic | Price |
|---|---|
| From the internet to the NAT: downloads, replies | free |
| From the NAT to the internet: uploads, requests | $0.09 per GB, for the first 10 TB a month |
| Between a client and the NAT in the same Availability Zone | free |
| Between a client and the NAT in different Availability Zones | $0.01 per GB at each end: $0.02 for every GB that crosses, in either direction |
| To S3, DynamoDB, SQS, SNS, SES, Kinesis, ECR, Glacier or the EBS direct APIs in the same Region | free |
| To or from another public or Elastic IPv4 address in the same Region (an instance or a load balancer, yours or someone else's) | $0.01 per GB in each direction, downloads included |
| To another AWS Region | $0.02 per GB to most Regions, $0.01 to US East (Ohio) |

- A GB that a client in another zone uploads through the NAT costs $0.02 + $0.09. A GB that it downloads costs $0.02.
- [NAT64](/docs/how-it-works/nat64/) traffic leaves as IPv4 from the same public address, and is billed as IPv4 traffic.

## CloudWatch

### Allowance metrics and alarm

With `conntrack_alarm_actions`, natkit publishes the [allowance counters](/docs/operate/monitoring/#allowance-metrics) and the module creates one alarm:

| | Price | A month |
|---|---|---|
| 2 custom metrics | $0.30 each | $0.60 |
| 1 alarm on one metric | $0.10 | $0.10 |
| `PutMetricData`: one request about every 50 seconds, about 52 000 a month | $0.01 per 1 000 | $0.52 |
| **Total** | | **$1.22** |

- Metrics are billed by the hour, and only for the hours in which data arrives.
- The metrics carry the module's `name`, not the instance ID. A replacement instance doesn't create new ones.
- With one NAT per zone, each NAT has its own name and its own $1.22.
- What the alarm notifies, such as an SNS topic, is billed separately.

### The CloudWatch agent

Every classic metric the [agent](/docs/operate/monitoring/#the-cloudwatch-agent) publishes is a custom metric: $0.30 a month each, plus its `PutMetricData` requests, about $0.44 a month at one request a minute. Each combination of metric name and dimensions is one metric.

- The `ethtool` example in [Performance](/docs/operate/monitoring/#alarms) has 5 counters on 2 interfaces: 10 metrics, $3.00 a month.
- With an `InstanceId` dimension, every replacement instance starts new metrics. They are billed by the hour, so the old ones stop costing when the old instance stops.
- Logs, if the agent's config collects them, are billed per GB ingested and per GB stored.
- The agent needs 1 GiB of memory, a micro instead of a nano: $3.07 a month more on T4g, $3.80 on T3.

## The AMI

The AMIs aren't public: you [import the disk image](/docs/start/getting-started/#get-the-ami) into your own account, and the snapshot behind the AMI is yours to pay.

- A snapshot is billed for the data it holds, not for the volume's size. natkit's holds about 0.1 GB: about half a cent a month for each AMI you keep, in each Region.
- Deregistering an AMI doesn't delete its snapshot. To stop paying, delete the snapshot too.
- `scripts/ami.sh` uploads the image, about 0.1 GB, to your S3 bucket and removes it when it exits. On a bucket with versioning the removed object stays as a noncurrent version, and is billed until a lifecycle rule expires it.
- The import itself is free.

## Replacements

A new AMI or new settings [replace the instance](/docs/deploy/upgrading/). Two instances run side by side for two or three minutes. The second instance, its volume and its public IPv4 address are billed by the second: less than a tenth of a cent, plus the surplus CPU credits the old instance still owes.

A replacement that [fails](/docs/how-it-works/high-availability/#when-the-replacement-fails) is different. Each failed instance stays for 5 minutes before the group terminates it and launches the next, so a second instance is running most of the time until the configuration is fixed: about $0.32 a day on a `t4g.micro` for the instance, its volume and its address, plus the surplus credits each one spends booting.

## One NAT per Availability Zone

With [one NAT per zone](/docs/how-it-works/high-availability/#one-nat-per-availability-zone), every fixed cost above is paid once per zone. Three zones cost 3 × $9.86 = $29.58 a month with the default `t4g.micro`, and 3 × $6.80 = $20.40 on `t4g.nano`.

With one NAT for all zones, the traffic of the clients in the other zones pays $0.02 per GB between zones, uploads and downloads alike. A second NAT for a zone costs less than that from 493 GB a month of that zone's traffic on a `t4g.micro`, and from 340 GB on a `t4g.nano`.

## Compared with a NAT gateway

A NAT gateway costs $0.045 an hour ($32.85 a month), plus its public IPv4 address ($3.65), plus $0.045 for every GB it processes in either direction. The data transfer prices above apply to both.

| Egress in a month | NAT gateway | natkit on `t4g.micro` (default) | natkit on `t4g.nano` |
|---|---|---|---|
| 10 GB | $36.50 + 10 × $0.045 + 10 × $0.09 = $37.85 | $9.86 + 10 × $0.09 = $10.76 | $6.80 + 10 × $0.09 = $7.70 |
| 100 GB | $36.50 + 100 × $0.045 + 100 × $0.09 = $50.00 | $9.86 + 100 × $0.09 = $18.86 | $6.80 + 100 × $0.09 = $15.80 |
| 1 000 GB | $36.50 + 1 000 × $0.045 + 1 000 × $0.09 = $171.50 | $9.86 + 1 000 × $0.09 = $99.86 | $6.80 + 1 000 × $0.09 = $96.80 |

- The NAT gateway's $0.045 per GB also applies to downloads and to traffic to AWS services in the same Region. Through natkit those cost nothing per GB.
- natkit's columns leave out surplus CPU credits. 1 000 GB a month is about 3 Mbit/s on average.
- A NAT gateway bills a partial hour as a full hour.
- The price is not the only difference. See [Performance](/docs/operate/performance/) for a NAT instance's limits, and [High availability](/docs/how-it-works/high-availability/) for what a replacement interrupts.

## Free tiers and discounts

The figures above leave these out:

- **CloudWatch:** 10 custom metrics, 10 alarm metrics and 1 million API requests a month are free, and that doesn't expire. It is free once per account, or once per organization when the accounts share a bill. One NAT's allowance metrics and alarm use 2 metrics, 1 alarm metric and about 52 000 requests of it.
- **Data transfer:** the first 100 GB a month sent to the internet is free, counted across all services and Regions, per account or organization.
- **Savings Plans and Reserved Instances** lower only the instance's hourly price. The volume, the public IPv4 address, surplus credits, data transfer and CloudWatch stay at list price.

## Launched by hand

A single instance [launched without the module](/docs/deploy/by-hand/) is billed for the same three things: the instance, its volume and its public IPv4 address.

- Without an EIP it uses the address it launched with, at the same price.
- Stop it, and the instance charge stops and that address is released. The volume is still billed, and so is an EIP.
- Terminate it, and the volume is deleted. An EIP stays allocated, and billed, until you release it.
- To launch it in `standard` mode, add `--credit-specification CpuCredits=standard`.

## Building and testing

`make ami`, `scripts/boot-test.sh` and `scripts/module-test.sh` run real resources for the time of a test: a few small instances, an EIP, the metrics and an alarm. That is cents per run. `tofu/ci` keeps nothing that is billed.

## Elsewhere in your account

Some services charge by activity, and a NAT is busy:

- **VPC Flow Logs** record every flow through the NAT on both of its ENIs.
- **GuardDuty** analyses those flow logs by volume.
- **AWS Config** records each replaced instance, ENI and volume.
- **CloudTrail data events**, if you log them for CloudWatch, record each of natkit's metric reports.
