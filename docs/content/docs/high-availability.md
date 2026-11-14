---
title: High availability
description: An Auto Scaling group of one, a static ENI and an EIP.
weight: 30
icon: sync_alt
---

For HA, run natkit in an Auto Scaling group of one, with `eni_id` and `eip_id` in its user-data:

- **Static ENI (`eni_id`):** the private route tables point at this ENI, not at an instance. The instance attaches it as device 1.
- **EIP (`eip_id`):** the instance associates it with its primary ENI, so egress keeps the same public IP across replacements.

When an instance is replaced:

1. The new instance finishes its own setup first: nftables, sysctls, NAT64, its SSM parameters. If any of it fails, it takes neither the ENI nor the EIP.
2. It takes the ENI. If another instance holds it, natkit detaches it from that one (a forced detach) and attaches it.
3. Once it has the ENI, it takes the EIP.

The takeover needs `ec2:DescribeNetworkInterfaces` and `ec2:DetachNetworkInterface` in the [instance role](../instance-role/). Without them the new instance retries the attachment until the old one lets go, for up to 15 minutes, as fck-nat does.

fck-nat takes the EIP first. That moves egress off the EIP while the old instance is still routing. natkit takes the ENI first to avoid that gap.

The [OpenTofu module](../opentofu/) sets up this layout. Its Auto Scaling group launches the replacement before it terminates the old instance, so egress stops only for the handover: 14 seconds in a test. Connections open at that moment are cut, because the new instance doesn't know them.

## When the replacement fails

An instance whose setup fails prints `natkit: FAIL <reason>` on its console and takes neither the ENI nor the EIP. To EC2 it is still a healthy instance, so the Auto Scaling group would go on and terminate the old one, the only one routing.

natkit therefore reports itself unhealthy to its group (`autoscaling:SetInstanceHealth`, in the [instance role](../instance-role/)). The group terminates the failed instance and launches another, and the old instance keeps routing until a replacement comes up healthy. A setting that natkit refuses stops the rollout; it doesn't stop egress.

- The group keeps launching instances until the configuration is fixed. Each one runs for a minute or two.
- The failed instances are terminated, so read the reason from their console output: `aws ec2 get-console-output --instance-id <id>`, with the instance ID from the group's activity history. EC2 keeps it for about an hour after the termination.
- This needs natkit v0.1.4 or later on the new instance. An older image fails without telling the group.

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
  source   = "github.com/ulm0/natkit//tofu/nat?ref=v0.1.3"
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
