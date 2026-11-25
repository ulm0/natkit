---
title: Upgrading
description: How a new release reaches a running NAT, and what each one changes.
weight: 40
---

With the [OpenTofu module](/docs/deploy/opentofu/), a new AMI, new user-data or a change to the primary ENI's security group replaces the instance through an instance refresh of its Auto Scaling group. The replacement launches first, sets itself up and then [takes the ENI over](/docs/how-it-works/high-availability/): egress stops for the handover (usually 10 to 15 seconds, at most about 35 in the tests), and the connections open at that moment are cut. The group puts the new instance in service when natkit completes the [lifecycle hook](/docs/how-it-works/high-availability/#when-the-replacement-fails), and terminates the old one 90 seconds later. A replacement that fails its setup is never put in service, and the old instance stays.

Upgrade the module and the AMI together, from the same release. The module refuses an AMI that doesn't know the keys it sets: from v0.1.5 it sets `lifecycle_hook`, so it refuses the releases up to v0.1.3.

## How long an AMI stays up

For good. A release's public AMIs are never deregistered, so a launch template or an Auto Scaling group that names one can always launch from it, and its name (`natkit-<version>-<arch>`) always means the same AMI: natkit never registers a name twice.

AWS deprecates every public AMI two years after it's created (v0.1.5's in November 2028), and only a private AMI's deprecation can be put off. A deprecated AMI still launches by its ID. A lookup by name or tags skips it unless it asks for deprecated AMIs:

```hcl
data "aws_ami" "natkit" {
  owners             = ["846227790340"]
  include_deprecated = true # still found after its deprecation date
  filter {
    name   = "name"
    values = ["natkit-v0.1.5-arm64"]
  }
}
```

With the AWS CLI, `describe-images --include-deprecated`.

## Without the module

Without the module, launch an instance from the new AMI in place of the old one; with an Auto Scaling group of your own, put the new AMI in its launch template and start an instance refresh.

## To v0.1.9

Nothing changes in natkit itself: the module, natkit-init and the settings are those of v0.1.8. What changes is where the AMIs come from: v0.1.9 and later are published from account `846227790340`, and releases up to v0.1.8 came from `208689161537`, which is being retired. With the module, moving to v0.1.9's AMI replaces the instance once, as any new AMI does.

- Look the AMI up with the new owner: `owners = ["846227790340"]` in an `aws_ami` data source, `--owners 846227790340` in the CLI (see [Get the AMI](/docs/start/getting-started/#get-the-ami)). A lookup that still names `208689161537` finds v0.1.8 and older, never v0.1.9.
- v0.1.8's AMIs stay where they are for now; a published release's AMIs are not deregistered, but the old account is going away, so move to v0.1.9.

## To v0.1.8

From v0.1.7, with the module, moving to v0.1.8's AMI replaces the instance once, as any new AMI does. Nothing else changes unless you set the new input.

- natkit's [allowance metrics](/docs/operate/monitoring/#allowance-metrics) now include `bw_in_allowance_exceeded`, `bw_out_allowance_exceeded` and `pps_allowance_exceeded`: packets EC2 queued or dropped because the instance went over its bandwidth or packets-per-second allowance. They go in the same request, under the same namespace and dimension, and need no new permission. The module's alarm still watches `conntrack_allowance_exceeded` only; [Monitoring](/docs/operate/monitoring/#alarms) has an example alarm on the new ones.
- With `conntrack_alarm_actions` (or `metrics` in the user-data), CloudWatch now bills 5 custom metrics instead of 2: about $2.12 a month for each NAT instead of $1.22, with the alarm. See [Costs](/docs/operate/costs/#cloudwatch).
- The module has a new input, `cpu_credits` (`unlimited` or `standard`). Unset, the default, nothing changes: EC2's default for the instance family applies. See [CPU credits](/docs/operate/costs/#cpu-credits).
- New page: [From a NAT gateway](/docs/deploy/migrating/), to move from NAT gateways one zone at a time and keep their Elastic IPs.

## To v0.1.7

v0.1.6 was tagged but not released: its build failed, and v0.1.7 fixes the cause. From v0.1.5, nothing changes on a running NAT: the same settings, console lines and behaviour. With the module, moving to v0.1.7's AMI replaces the instance once, as any new AMI does.

- An AWS call whose answer arrived within microseconds could fail after AWS had carried it out (a race in the AWS SDK's HTTP layer, seen against the test mock only). natkit tries the call again, but an attach or a lifecycle hook's completion can't be done twice. natkit now sends its requests so that it can't happen.

- The AMIs are public in us-east-1 (owner `846227790340`), tagged `ec2:SharedTag/natkit.io/version`, `/arch` and `/commit`: an AMI you imported yourself there can go. Look the public one up with the owner.
- A published release's AMIs are never removed. Past AWS's two-year deprecation, look them up with `include_deprecated = true`.

## To v0.1.5

v0.1.4 was tagged but not released: its amd64 boot test failed, and v0.1.5 fixes the cause. From v0.1.3, with the module:

- The instance is replaced once: its user-data changes.
- The lifecycle hook is created along with that first replacement, so it may not hold it yet. It holds every one after.
- With `instance_profile`, add the Auto Scaling actions of the [instance role](/docs/reference/instance-role/) to your role before you apply, and the takeover actions (`ec2:DescribeNetworkInterfaces`, `ec2:DetachNetworkInterface`) if it doesn't have them. Without them the new instance fails its setup, and if the hook doesn't hold this first replacement yet, the group still terminates the old instance after the warmup: nothing routes.
- With `conntrack_alarm_actions`, natkit now publishes the counters itself. The metrics are `conntrack_allowance_exceeded` (packets dropped since the last report) and `conntrack_allowance_available`, with the dimension `Name`. They were `ethtool_conntrack_allowance_*`, with `AutoScalingGroupName`: change your own alarms and dashboards that use them. The module removes the agent's SSM parameter and `CloudWatchAgentServerPolicy` from its role, and its alarm reports once that it has no data, until the new instance publishes.
- Without `nf_conntrack_max` in `settings`, the kernel's connection table is now at least EC2's allowance: about 154 000 on a `t4g.nano` instead of 55 000, up to about 60 MB of memory when full. This is the AMI's default, so it holds without the module too, unless the user-data sets `nf_conntrack_max`.
- From v0.1.1 or earlier, the upgrade also splits the one security group into two.
