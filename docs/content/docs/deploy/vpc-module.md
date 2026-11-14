---
title: With the VPC module
description: natkit's module next to terraform-aws-modules/vpc.
weight: 20
aliases: [/docs/vpc-module/]
---

The community [VPC module](https://registry.terraform.io/modules/terraform-aws-modules/vpc/aws) makes the VPC, its subnets and route tables. Leave its NAT gateways off (`enable_nat_gateway = false`) and give natkit's [OpenTofu module](/docs/deploy/opentofu/) its outputs:

| natkit's input | The VPC module's output |
|---|---|
| `vpc_id` | `vpc_id` |
| `subnet_id` | one of `public_subnets` |
| `route_table_ids` | `private_route_table_ids` |

This is `examples/vpc` in the repository, which CI checks against the module. Outside the repository, `source` is `github.com/ulm0/natkit//tofu/nat?ref=v0.1.5`, and the AMI is the one you [imported](/docs/start/getting-started/).

The VPC module also takes over the VPC's default security group and leaves it without rules (`manage_default_security_group`): an instance launched with it can't send anything, through the NAT or otherwise. Give your instances a security group that allows their outbound traffic.

<!-- terraform-docs examples/vpc writes the example from examples/vpc/main.tf -->
<!-- BEGIN_TF_DOCS -->
```hcl
# natkit with the community VPC module: the VPC module makes the VPC, its subnets and route tables, without NAT
# gateways, and natkit's module routes every private route table through one NAT instance.
terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 6.68" }
  }
}

provider "aws" {
  region = "us-east-1"
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 6.7"

  name            = "main"
  cidr            = "10.0.0.0/16"
  azs             = ["us-east-1a", "us-east-1b"]
  public_subnets  = ["10.0.0.0/24", "10.0.1.0/24"]
  private_subnets = ["10.0.10.0/24", "10.0.11.0/24"]

  enable_nat_gateway = false # natkit instead
}

# natkit's AMI, imported into this account (Getting started), from the same release as the module
data "aws_ami" "natkit" {
  owners = ["self"]
  filter {
    name   = "name"
    values = ["natkit-v0.1.5-arm64"]
  }
}

module "nat" {
  source = "../../tofu/nat" # outside this repository: "github.com/ulm0/natkit//tofu/nat?ref=v0.1.5"

  vpc_id          = module.vpc.vpc_id
  subnet_id       = module.vpc.public_subnets[0]
  route_table_ids = module.vpc.private_route_table_ids
  ami_id          = data.aws_ami.natkit.id
}

output "nat_public_ip" {
  description = "The private subnets' egress address."
  value       = module.nat.public_ip
}
```
<!-- END_TF_DOCS -->

## One NAT per Availability Zone

The VPC module makes one private route table per private subnet, in the order of `azs`, and puts the public subnets in the same order. For [one NAT per zone](/docs/how-it-works/high-availability/#one-nat-per-availability-zone), each zone's NAT goes in that zone's public subnet and takes that zone's route table:

```hcl
module "nat" {
  source   = "github.com/ulm0/natkit//tofu/nat?ref=v0.1.5"
  for_each = { for i, az in module.vpc.azs : az => i }

  name            = "natkit-${each.key}"
  vpc_id          = module.vpc.vpc_id
  subnet_id       = module.vpc.public_subnets[each.value]
  route_table_ids = [module.vpc.private_route_table_ids[each.value]]
  ami_id          = data.aws_ami.natkit.id
}
```

Each NAT is billed on its own: see [Costs](/docs/operate/costs/#one-nat-per-availability-zone).
