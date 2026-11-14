# Fixture of scripts/module-test.sh: tofu/nat in the test VPC, routing the private subnet through the NAT, and a
# client there whose serial console shows whether it gets out. The state is in S3 (init -backend-config), so CI's
# cleanup job can destroy what a cancelled run left behind.
terraform {
  required_version = ">= 1.6"
  backend "s3" {
    use_lockfile = true
  }
}

variable "ami_id" {
  type = string
}
variable "arch" {
  description = "arm64 or amd64: the AMI's."
  type        = string
}
variable "public_subnet" {
  type = string
}
variable "private_subnet" {
  description = "With its own route table and no other egress."
  type        = string
}
variable "instance_profile" {
  description = "The NAT's, made ahead (tofu/ci): CI has no IAM permissions to make one."
  type        = string
}
variable "tag" {
  type = string
}
variable "client_cidrs" {
  type    = list(string)
  default = null
}
variable "untracked" {
  type    = bool
  default = false
}
variable "tcp_established_timeout" {
  type    = number
  default = null
}
variable "keepalive" {
  description = "A natkit setting: changing it changes the user-data, so the instance is refreshed."
  type        = number
  default     = 600
}

data "aws_subnet" "public" {
  id = var.public_subnet
}
data "aws_vpc" "this" {
  id = data.aws_subnet.public.vpc_id
}
data "aws_route_table" "private" {
  subnet_id = var.private_subnet
}
data "aws_ssm_parameter" "client_ami" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-minimal-kernel-default-${var.arch == "arm64" ? "arm64" : "x86_64"}"
}

locals {
  tags = { "natkit.io/module-test" = var.tag }
  type = var.arch == "arm64" ? "t4g.micro" : "t3.micro"
}

module "nat" {
  source                  = "../../tofu/nat"
  name                    = "natkit-module-test"
  vpc_id                  = data.aws_vpc.this.id
  subnet_id               = var.public_subnet
  route_table_ids         = [data.aws_route_table.private.id]
  ami_id                  = var.ami_id
  instance_type           = local.type
  instance_profile        = var.instance_profile
  nat64                   = true
  ipv6_cidr               = data.aws_vpc.this.ipv6_cidr_block
  client_cidrs            = var.client_cidrs
  untracked               = var.untracked
  tcp_established_timeout = var.tcp_established_timeout
  conntrack_alarm_actions = []
  settings                = { tcp_keepalive_time = var.keepalive }
  tags                    = local.tags
}

# no security group named: the VPC's default one, which admits traffic from itself only
resource "aws_instance" "client" {
  ami                = nonsensitive(data.aws_ssm_parameter.client_ami.value)
  instance_type      = local.type
  subnet_id          = var.private_subnet
  ipv6_address_count = 1
  user_data          = file("${path.module}/client.sh")
  tags               = merge(local.tags, { Name = "natkit-module-test-client" })
  depends_on         = [module.nat]

  metadata_options {
    http_tokens = "required"
  }
}

output "client" {
  value = aws_instance.client.id
}
output "eip" {
  value = module.nat.public_ip
}
output "asg" {
  value = module.nat.autoscaling_group_name
}
output "alarm" {
  value = module.nat.conntrack_alarm_name
}
output "public_sg" {
  value = module.nat.public_security_group_id
}
output "private_sg" {
  value = module.nat.security_group_id
}
