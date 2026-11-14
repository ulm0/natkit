---
title: OpenTofu module
description: tofu/nat sets up the HA layout.
weight: 10
aliases: [/docs/opentofu/]
---

`tofu/nat` sets up the [HA layout](/docs/how-it-works/high-availability/) as a module:

- an Auto Scaling group of one, with a [launch lifecycle hook](/docs/how-it-works/high-availability/#when-the-replacement-fails): a replacement is in service when natkit says it's ready, not before;
- a static ENI and an EIP;
- the private route tables' routes;
- a scoped instance role.

```hcl
module "nat" {
  source          = "github.com/ulm0/natkit//tofu/nat?ref=v0.1.4"
  vpc_id          = aws_vpc.main.id
  subnet_id       = aws_subnet.public.id
  route_table_ids = [aws_route_table.private.id]
  ami_id          = "ami-..." # natkit-v0.1.4-arm64: the same release as the module
  instance_type   = "t4g.micro"
  settings        = { nf_conntrack_max = "262144" }
}
```

With the community VPC module, see [With the VPC module](/docs/deploy/vpc-module/). Once applied, [check that it's up](/docs/start/getting-started/#check-that-its-up). [Upgrading](/docs/deploy/upgrading/) says how a new release rolls out and what each one changes.

<!-- terraform-docs tofu/nat writes Inputs and Outputs from the module's descriptions -->
<!-- BEGIN_TF_DOCS -->
## Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| vpc\_id | The VPC. | `string` | n/a | yes |
| subnet\_id | Public subnet (internet gateway route) for the instance and its static ENI. | `string` | n/a | yes |
| route\_table\_ids | Private route tables that get `0.0.0.0/0` (and `64:ff9b::/96` with `nat64`) to the static ENI. | `list(string)` | n/a | yes |
| ami\_id | A natkit AMI of the instance type's architecture, from the same release as the module. | `string` | n/a | yes |
| name | Name and `natkit` tag of every resource; with `conntrack_alarm_actions`, the `Name` dimension of the metrics. | `string` | `"natkit"` | no |
| instance\_type | 0.5 GiB of memory is enough (`t4g.nano`, `t3.nano`); the CloudWatch agent (`cwagent_cfg_param_name`) needs 1 GiB, and the module refuses a `*.nano` with it. See [instance size](https://natkit.ulm0.com/docs/start/getting-started/#instance-size). | `string` | `"t4g.micro"` | no |
| eip | Allocate an EIP and have the instance take it. Otherwise it egresses from its own public IP, which changes on replacement. | `bool` | `true` | no |
| nat64 | Route `64:ff9b::/96` to the ENI and give it an IPv6 address (the subnet needs an IPv6 CIDR). Pair with Route 53 Resolver DNS64: see [NAT64](https://natkit.ulm0.com/docs/how-it-works/nat64/). | `bool` | `false` | no |
| ipv6\_cidr | The VPC's IPv6 CIDR, a default client with `nat64`. | `string` | `null` | no |
| client\_cidrs | Sources that may route through the NAT, IPv4 and IPv6, in canonical form. Enforced by the static ENI's security group (unless `untracked`) and by natkit's nftables ([`client_cidrs`](https://natkit.ulm0.com/docs/reference/configuration/)). Default: the VPC's IPv4 CIDRs, plus `ipv6_cidr`. | `list(string)` | `null` | no |
| untracked | Open the static ENI's security group to all traffic, so EC2 doesn't track the flows through it: about twice the connections before EC2's per-instance allowance runs out. `client_cidrs` is then enforced by natkit's nftables only. See [Performance](https://natkit.ulm0.com/docs/operate/performance/). | `bool` | `false` | no |
| tcp\_established\_timeout | EC2's idle timeout, in seconds (60 to 432000), for established TCP connections it tracks on both ENIs. Default: EC2's (432000, or 350 on Nitro v6 instances). 350 matches NAT Gateway and frees the allowance from abandoned connections sooner. Going back to `null` leaves the static ENI at its last value: set EC2's default explicitly instead. See [Performance](https://natkit.ulm0.com/docs/operate/performance/). | `number` | `null` | no |
| settings | natkit's tuning [user-data keys](https://natkit.ulm0.com/docs/reference/configuration/): `ip_local_port_range`, `nf_conntrack_max`, `nf_conntrack_buckets`, `nf_conntrack_tcp_timeout_established`, `tcp_keepalive_time`, `tcp_max_syn_backlog`. Any other key is refused at plan time. | `map(any)` | `{}` | no |
| cwagent\_cfg\_param\_name | SSM parameter with a [CloudWatch agent](https://natkit.ulm0.com/docs/operate/monitoring/) JSON config: enables the agent. Needs 1 GiB of memory: a micro or larger. | `string` | `null` | no |
| conntrack\_alarm\_actions | Alarm when EC2 drops packets because the instance's [connection-tracking allowance](https://natkit.ulm0.com/docs/operate/performance/) ran out, and when the NAT stops reporting, with these actions (SNS topic ARNs; `[]` for an alarm without any). natkit publishes the ENA driver's allowance counters itself ([`metrics`](https://natkit.ulm0.com/docs/operate/monitoring/#allowance-metrics)), without the CloudWatch agent. CloudWatch bills the two metrics, the requests and the alarm: see [Costs](https://natkit.ulm0.com/docs/operate/costs/). | `list(string)` | `null` | no |
| instance\_profile | Name of an existing instance profile to use instead of the module's role, where IAM is managed elsewhere. Its role needs the [instance role](https://natkit.ulm0.com/docs/reference/instance-role/)'s permissions. | `string` | `null` | no |
| console\_password\_param | SSM parameter with root's crypt hash (`openssl passwd -6`) for the EC2 Serial Console. | `string` | `null` | no |
| tags | Extra tags for every resource. | `map(string)` | `{}` | no |

## Outputs

| Name | Description |
| ---- | ----------- |
| eni\_id | The static ENI: the private route tables' target. |
| public\_ip | The EIP, with `eip = true`. |
| security\_group\_id | The static ENI's: what the clients' traffic must pass. |
| public\_security\_group\_id | The instance's primary (public) ENI's. |
| autoscaling\_group\_name | The Auto Scaling group of one. |
| conntrack\_alarm\_name | The alarm, with `conntrack_alarm_actions`. |
| role\_name | The instance role this module created; null with `instance_profile`. |
<!-- END_TF_DOCS -->

## Security groups

| ENI | Inbound | Outbound |
|---|---|---|
| Primary (public) | nothing: replies to the NAT's own connections only | all |
| Static (the route tables' target) | `client_cidrs`, or all with `untracked` | all |

The static ENI has to admit the clients: to EC2, a packet a client sends to the internet through the NAT is inbound traffic on that ENI.
