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
