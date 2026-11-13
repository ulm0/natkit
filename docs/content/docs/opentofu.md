---
title: OpenTofu module
description: tofu/nat sets up the HA layout.
weight: 50
icon: deployed_code
---

`tofu/nat` sets up the [HA layout](../high-availability/) as a module:

- an Auto Scaling group of one;
- a static ENI and an EIP;
- the private route tables' routes;
- a scoped instance role.

```hcl
module "nat" {
  source          = "github.com/ulm0/natkit//tofu/nat"
  vpc_id          = aws_vpc.main.id
  subnet_id       = aws_subnet.public.id
  route_table_ids = [aws_route_table.private.id]
  ami_id          = "ami-..." # natkit-<version>-arm64
  instance_type   = "t4g.micro"
  settings        = { nf_conntrack_max = "262144" }
}
```

## Inputs

| Name | Default | |
|---|---|---|
| `vpc_id` | | the VPC |
| `subnet_id` | | public subnet (internet gateway route) for the instance and its static ENI |
| `route_table_ids` | | private route tables that get `0.0.0.0/0` (and `64:ff9b::/96` with `nat64`) to the static ENI |
| `ami_id` | | a natkit AMI of the instance type's architecture |
| `name` | `natkit` | name and `natkit` tag of every resource |
| `instance_type` | `t4g.micro` | see [instance size](../getting-started/#instance-size): `*.nano` sizes are rejected |
| `eip` | `true` | allocate an EIP and have the instance take it. Otherwise it egresses from its own public IP, which changes on replacement |
| `nat64` | `false` | route `64:ff9b::/96` to the ENI and give it an IPv6 address (the subnet needs an IPv6 CIDR) |
| `ipv6_cidr` | `null` | the VPC's IPv6 CIDR, admitted by the security group: needed with `nat64` |
| `settings` | `{}` | more [user-data keys](../configuration/) |
| `cwagent_cfg_param_name` | `null` | SSM parameter with a CloudWatch agent JSON config: enables the agent |
| `console_password_param` | `null` | SSM parameter with root's crypt hash for the EC2 Serial Console |
| `tags` | `{}` | extra tags |

## Outputs

`eni_id`, `public_ip` (the EIP, with `eip = true`), `security_group_id`, `role_name`.

## Updates

A new AMI or new user-data replaces the instance through an instance refresh. There is only one instance, so egress stops until the new one has the ENI.
