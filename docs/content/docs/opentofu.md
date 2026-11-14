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
  source          = "github.com/ulm0/natkit//tofu/nat?ref=v0.1.2"
  vpc_id          = aws_vpc.main.id
  subnet_id       = aws_subnet.public.id
  route_table_ids = [aws_route_table.private.id]
  ami_id          = "ami-..." # natkit-v0.1.2-arm64: the same release as the module
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
| `ipv6_cidr` | `null` | the VPC's IPv6 CIDR, a default client with `nat64` |
| `client_cidrs` | the VPC's IPv4 CIDRs, plus `ipv6_cidr` | sources that may route through the NAT, in canonical form: the static ENI's security group (unless `untracked`) and natkit's [`client_cidrs`](../configuration/) |
| `untracked` | `false` | open the static ENI's security group to all traffic, so EC2 doesn't track the flows through it. See [Performance](../performance/) |
| `tcp_established_timeout` | EC2's | EC2's idle timeout in seconds (60 to 432 000) for the established TCP connections it tracks, on both ENIs. Going back to `null` leaves the static ENI at its last value. See [Performance](../performance/) |
| `settings` | `{}` | more [user-data keys](../configuration/) |
| `cwagent_cfg_param_name` | `null` | SSM parameter with a CloudWatch agent JSON config: enables the agent |
| `console_password_param` | `null` | SSM parameter with root's crypt hash for the EC2 Serial Console |
| `tags` | `{}` | extra tags |

## Outputs

`eni_id`, `public_ip` (the EIP, with `eip = true`), `security_group_id` (the static ENI's), `public_security_group_id` (the instance's primary ENI's), `role_name`.

## Security groups

| ENI | Inbound | Outbound |
|---|---|---|
| Primary (public) | nothing: replies to the NAT's own connections only | all |
| Static (the route tables' target) | `client_cidrs`, or all with `untracked` | all |

The static ENI has to admit the clients: to EC2, a packet a client sends to the internet through the NAT is inbound traffic on that ENI.

## Updates

A new AMI, new user-data or a change to the primary ENI's security group replaces the instance through an instance refresh. There is only one instance, so egress stops until the new one has the ENI.

Upgrade the module and the AMI together, from the same release. Since v0.1.2 the module always sets `client_cidrs`, which natkit v0.1.1 and earlier refuse as an unknown key: the replacement instance would never take the ENI. The module refuses an AMI named `natkit-v0.1.0-*` or `natkit-v0.1.1-*`. The upgrade also splits the one security group into two.
