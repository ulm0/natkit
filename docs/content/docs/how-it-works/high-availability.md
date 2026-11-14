---
title: High availability
description: An Auto Scaling group of one, a static ENI and an EIP.
weight: 10
aliases: [/docs/high-availability/]
icon: sync_alt
---

For HA, run natkit in an Auto Scaling group of one, with `eni_id` and `eip_id` in its user-data:

- **Static ENI (`eni_id`):** the private route tables point at this ENI, not at an instance. The instance attaches it as device 1.
- **EIP (`eip_id`):** the instance associates it with its primary ENI, so egress keeps the same public IP across replacements.

When an instance is replaced:

1. The new instance finishes its own setup first: nftables, sysctls, NAT64, its SSM parameters. If any of it fails, it takes neither the ENI nor the EIP.
2. It takes the ENI. If another instance holds it, natkit detaches it from that one (a forced detach) and attaches it.
3. Once it has the ENI, it tells its group that it is in service ([`lifecycle_hook`](#when-the-replacement-fails)) and takes the EIP.

The takeover needs `ec2:DescribeNetworkInterfaces` and `ec2:DetachNetworkInterface` in the [instance role](/docs/reference/instance-role/). Without them the new instance retries the attachment until the old one lets go, for up to 15 minutes, as fck-nat does.

fck-nat takes the EIP first. That moves egress off the EIP while the old instance is still routing. natkit takes the ENI first to avoid that gap.

The [OpenTofu module](/docs/deploy/opentofu/) sets up this layout. Its Auto Scaling group launches the replacement before it terminates the old instance, so egress stops only for the handover: 14 seconds in a test. Connections open at that moment are cut, because the new instance doesn't know them.

## When the replacement fails

To EC2, an instance is healthy as soon as it runs. An Auto Scaling group that goes by that alone would launch a replacement, see it running, and terminate the old instance, even if the new one never came up as a NAT: a setting that natkit refuses, an image that doesn't boot. Nothing would route.

A launch lifecycle hook closes that gap. The group holds a new instance out of service until natkit completes the hook, and keeps the old instance until then:

```yaml
lifecycle_hook: natkit-ready   # the hook's name
```

1. natkit finishes its setup, finds its group and checks that it can reach the hook. If it can't, it stops there and takes nothing.
2. It takes the ENI. From here on it is the instance that routes.
3. It completes the hook. The group puts the instance in service and, after the warmup, terminates the old one.
4. It takes the EIP.

An instance that fails before step 2, or never boots, doesn't complete the hook. The group terminates it when the hook times out and launches another. The old instance keeps routing in the meantime, so a bad setting stops the rollout, not egress.

- The failed instance stays for the hook's timeout (5 minutes with the module). Its console says why: `natkit: FAIL <reason>`.
- An instance refresh doesn't try forever: after a while Auto Scaling marks it as failed and stops launching. The old instance stays. Fix the setting and apply again.
- Each attempt is billed like any instance: its minutes, its volume and its public IPv4 address. Auto Scaling slows the launches down when a hook keeps failing.
- If it can't complete the hook, it routes until the group terminates it at the hook's timeout. The old instance is still in service but no longer has the ENI, so egress stops until the next attempt takes it. Its console says `natkit: FAIL`.
- A failure after step 3 leaves the new instance in service, because it is the only one that can still route. If it can't take the EIP, it egresses from its own public IP and its console says `natkit: FAIL`.
- The instance role needs `autoscaling:DescribeAutoScalingInstances`, `autoscaling:RecordLifecycleActionHeartbeat` and `autoscaling:CompleteLifecycleAction`: see [instance role](/docs/reference/instance-role/).
- With the hook, the takeover permissions are required too: the old instance is not terminated first, so a new one that can't take the ENI over fails instead of waiting.

The [OpenTofu module](/docs/deploy/opentofu/) creates the hook and sets the key. For a group of your own:

```sh
aws autoscaling put-lifecycle-hook --auto-scaling-group-name <group> --lifecycle-hook-name natkit-ready \
  --lifecycle-transition autoscaling:EC2_INSTANCE_LAUNCHING --heartbeat-timeout 300 --default-result ABANDON
```

Without the key, natkit doesn't call Auto Scaling at all.

## One NAT per Availability Zone

The ASG keeps one instance alive, but that instance lives in a single AZ: if the AZ goes down, every private subnet loses egress until it comes back. For AZ-independent egress, like one NAT Gateway per AZ, run one natkit per AZ. The module already covers one AZ (one `subnet_id`), so instantiate it once per AZ:

```hcl
locals {
  azs = {
    a = { public_subnet = aws_subnet.public_a.id, private_rts = [aws_route_table.private_a.id] }
    b = { public_subnet = aws_subnet.public_b.id, private_rts = [aws_route_table.private_b.id] }
  }
}

module "nat" {
  source   = "github.com/ulm0/natkit//tofu/nat?ref=v0.1.4"
  for_each = local.azs

  name            = "nat-${each.key}"
  vpc_id          = aws_vpc.main.id
  subnet_id       = each.value.public_subnet
  route_table_ids = each.value.private_rts
  ami_id          = var.natkit_ami
}
```

- **One private route table per AZ.** A route table has a single `0.0.0.0/0` route, so subnets that share one can only use one NAT.
- **A unique `name` per instance.** Each instance role can only act on resources tagged `natkit = <name>`, so with distinct names each NAT can only move its own ENI and EIP.

Each AZ gets its own ASG, static ENI, EIP, security group and role, so you pay one instance and one EIP per AZ.
