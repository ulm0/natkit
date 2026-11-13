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

1. The new instance keeps retrying the ENI attachment while the old one still holds it, for up to 15 minutes.
2. Once it has the ENI, it takes the EIP.

fck-nat takes the EIP first. That moves egress off the EIP while the old instance is still routing. natkit waits for the ENI first to avoid that gap.

The [OpenTofu module](../opentofu/) sets up this layout.

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
  source   = "github.com/ulm0/natkit//tofu/nat?ref=v0.1.0"
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
