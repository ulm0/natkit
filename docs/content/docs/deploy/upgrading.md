---
title: Upgrading
description: How a new release reaches a running NAT, and what each one changes.
weight: 40
---

With the [OpenTofu module](/docs/deploy/opentofu/), a new AMI, new user-data or a change to the primary ENI's security group replaces the instance through an instance refresh of its Auto Scaling group. The replacement launches first, sets itself up and then [takes the ENI over](/docs/how-it-works/high-availability/): egress stops for the handover (14 seconds in a test), and the connections open at that moment are cut. The group puts the new instance in service when natkit completes the [lifecycle hook](/docs/how-it-works/high-availability/#when-the-replacement-fails), and terminates the old one 90 seconds later. A replacement that fails its setup is never put in service, and the old instance stays.

Upgrade the module and the AMI together, from the same release. The module refuses an AMI that doesn't know the keys it sets: from v0.1.5 it sets `lifecycle_hook`, so it refuses the releases up to v0.1.3.

Without the module, launch an instance from the new AMI in place of the old one; with an Auto Scaling group of your own, put the new AMI in its launch template and start an instance refresh.

## To v0.1.5

v0.1.4 was tagged but not released: its amd64 boot test failed, and v0.1.5 fixes the cause. From v0.1.3, with the module:

- The instance is replaced once: its user-data changes.
- The lifecycle hook is created along with that first replacement, so it may not hold it yet. It holds every one after.
- With `instance_profile`, add the Auto Scaling actions of the [instance role](/docs/reference/instance-role/) to your role before you apply, and the takeover actions (`ec2:DescribeNetworkInterfaces`, `ec2:DetachNetworkInterface`) if it doesn't have them. Without them the new instance fails its setup, and if the hook doesn't hold this first replacement yet, the group still terminates the old instance after the warmup: nothing routes.
- With `conntrack_alarm_actions`, natkit now publishes the counters itself. The metrics are `conntrack_allowance_exceeded` (packets dropped since the last report) and `conntrack_allowance_available`, with the dimension `Name`. They were `ethtool_conntrack_allowance_*`, with `AutoScalingGroupName`: change your own alarms and dashboards that use them. The module removes the agent's SSM parameter and `CloudWatchAgentServerPolicy` from its role, and its alarm reports once that it has no data, until the new instance publishes.
- Without `nf_conntrack_max` in `settings`, the kernel's connection table is now at least EC2's allowance: about 154 000 on a `t4g.nano` instead of 55 000, up to about 60 MB of memory when full. This is the AMI's default, so it holds without the module too, unless the user-data sets `nf_conntrack_max`.
- From v0.1.1 or earlier, the upgrade also splits the one security group into two.
