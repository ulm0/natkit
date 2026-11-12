# A natkit NAT instance in fck-nat's HA layout: an Auto Scaling group of one, a static ENI the private route tables
# point at (the replacement instance re-attaches it), and optionally an EIP. User-data is natkit's YAML.
#
#   module "nat" {
#     source          = "github.com/ulm0/natkit//tofu/nat"
#     vpc_id          = aws_vpc.main.id
#     subnet_id       = aws_subnet.public.id
#     route_table_ids = [aws_route_table.private.id]
#     ami_id          = "ami-..." # natkit-<version>-arm64
#   }
terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 6.0" }
  }
}

variable "name" {
  type    = string
  default = "natkit"
}
variable "vpc_id" {
  type = string
}
variable "subnet_id" {
  description = "Public subnet (internet gateway route) for the instance and its static ENI."
  type        = string
}
variable "route_table_ids" {
  description = "Private route tables that get 0.0.0.0/0 (and 64:ff9b::/96 with nat64) -> the static ENI."
  type        = list(string)
}
variable "ami_id" {
  description = "A natkit AMI of the instance type's architecture."
  type        = string
}
variable "instance_type" {
  type    = string
  default = "t4g.nano"
}
variable "eip" {
  description = "Allocate an EIP and have the instance take it. Otherwise it egresses from its own public IP, which changes on replacement."
  type        = bool
  default     = true
}
variable "nat64" {
  description = "Route 64:ff9b::/96 to the ENI and give it an IPv6 address (the subnet needs an IPv6 CIDR). Pair with Route 53 Resolver DNS64."
  type        = bool
  default     = false
}
variable "ipv6_cidr" {
  description = "The VPC's IPv6 CIDR, admitted by the security group: needed with nat64."
  type        = string
  default     = null
}
variable "settings" {
  description = "More natkit user-data keys: nf_conntrack_max, ip_local_port_range, tcp_keepalive_time, ..."
  type        = map(any)
  default     = {}
}
variable "cwagent_cfg_param_name" {
  description = "SSM parameter with a CloudWatch agent JSON config: enables the agent. Use at least a micro instance."
  type        = string
  default     = null
}
variable "console_password_param" {
  description = "SSM parameter with root's crypt hash (openssl passwd -6) for the EC2 Serial Console."
  type        = string
  default     = null
}
variable "tags" {
  type    = map(string)
  default = {}
}

data "aws_vpc" "this" {
  id = var.vpc_id
}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  tags      = merge(var.tags, { Name = var.name, natkit = var.name })
  arn       = "arn:${data.aws_partition.current.partition}"
  ec2_arn   = "${local.arn}:ec2:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}"
  ssm_names = compact([var.cwagent_cfg_param_name, var.console_password_param])
  user_data = merge(
    var.settings,
    { eni_id = aws_network_interface.static.id },
    var.eip ? { eip_id = aws_eip.this[0].allocation_id } : {},
    var.nat64 ? {} : { nat64 = false },
    var.cwagent_cfg_param_name == null ? {} : { cwagent_enabled = true, cwagent_cfg_param_name = var.cwagent_cfg_param_name },
    var.console_password_param == null ? {} : { console_password = var.console_password_param },
  )
}

# everything in the VPC may route through the NAT; nothing from outside reaches it (and natkit itself drops
# inbound connections)
resource "aws_security_group" "this" {
  name_prefix = "${var.name}-"
  description = "natkit NAT instance"
  vpc_id      = var.vpc_id
  tags        = local.tags

  ingress {
    description      = "VPC"
    protocol         = "-1"
    from_port        = 0
    to_port          = 0
    cidr_blocks      = [data.aws_vpc.this.cidr_block]
    ipv6_cidr_blocks = var.ipv6_cidr == null ? [] : [var.ipv6_cidr]
  }
  egress {
    protocol         = "-1"
    from_port        = 0
    to_port          = 0
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_network_interface" "static" {
  description        = "${var.name} static ENI (route target)"
  subnet_id          = var.subnet_id
  security_groups    = [aws_security_group.this.id]
  source_dest_check  = false
  ipv6_address_count = var.nat64 ? 1 : 0
  tags               = local.tags
}

resource "aws_eip" "this" {
  count  = var.eip ? 1 : 0
  domain = "vpc"
  tags   = local.tags
}

resource "aws_route" "ipv4" {
  count                  = length(var.route_table_ids)
  route_table_id         = var.route_table_ids[count.index]
  destination_cidr_block = "0.0.0.0/0"
  network_interface_id   = aws_network_interface.static.id
}
resource "aws_route" "nat64" {
  count                       = var.nat64 ? length(var.route_table_ids) : 0
  route_table_id              = var.route_table_ids[count.index]
  destination_ipv6_cidr_block = "64:ff9b::/96"
  network_interface_id        = aws_network_interface.static.id
}

# the instance role: only what natkit-init calls, on this module's resources (the instance and its ENIs carry the
# natkit tag from the launch template)
resource "aws_iam_role" "this" {
  name_prefix = "${var.name}-"
  tags        = local.tags
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "sts:AssumeRole", Principal = { Service = "ec2.amazonaws.com" } }]
  })
}
resource "aws_iam_role_policy" "this" {
  name = "natkit"
  role = aws_iam_role.this.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat(
      [{
        Sid       = "OwnInterfaces"
        Effect    = "Allow"
        Action    = ["ec2:AttachNetworkInterface", "ec2:ModifyNetworkInterfaceAttribute", "ec2:AssociateAddress"]
        Resource  = ["${local.ec2_arn}:network-interface/*", "${local.ec2_arn}:instance/*"]
        Condition = { StringEquals = { "aws:ResourceTag/natkit" = var.name } }
      }],
      var.eip ? [{
        Sid      = "OwnEip"
        Effect   = "Allow"
        Action   = "ec2:AssociateAddress"
        Resource = "${local.ec2_arn}:elastic-ip/${aws_eip.this[0].allocation_id}"
      }] : [],
      length(local.ssm_names) > 0 ? [{
        Sid      = "OwnParameters"
        Effect   = "Allow"
        Action   = "ssm:GetParameter"
        Resource = [for n in local.ssm_names : "${local.arn}:ssm:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:parameter/${trimprefix(n, "/")}"]
      }] : [],
    )
  })
}
resource "aws_iam_role_policy_attachment" "cwagent" {
  count      = var.cwagent_cfg_param_name == null ? 0 : 1
  role       = aws_iam_role.this.name
  policy_arn = "${local.arn}:iam::aws:policy/CloudWatchAgentServerPolicy"
}
resource "aws_iam_instance_profile" "this" {
  name_prefix = "${var.name}-"
  role        = aws_iam_role.this.name
  tags        = local.tags
}

resource "aws_launch_template" "this" {
  name_prefix   = "${var.name}-"
  image_id      = var.ami_id
  instance_type = var.instance_type
  user_data     = base64encode(yamlencode(local.user_data))
  tags          = local.tags

  iam_instance_profile {
    arn = aws_iam_instance_profile.this.arn
  }
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }
  # a public IP at launch: natkit-init reaches the EC2 API with it before it has the EIP
  network_interfaces {
    associate_public_ip_address = true
    security_groups             = [aws_security_group.this.id]
    delete_on_termination       = true
    ipv6_address_count          = var.nat64 ? 1 : 0
  }
  block_device_mappings {
    device_name = "/dev/xvda"
    ebs {
      encrypted   = true
      volume_type = "gp3"
    }
  }
  dynamic "tag_specifications" {
    for_each = ["instance", "network-interface", "volume"]
    content {
      resource_type = tag_specifications.value
      tags          = local.tags
    }
  }
}

resource "aws_autoscaling_group" "this" {
  name_prefix         = "${var.name}-"
  min_size            = 1
  max_size            = 1
  desired_capacity    = 1
  vpc_zone_identifier = [var.subnet_id]
  health_check_type   = "EC2"

  launch_template {
    id      = aws_launch_template.this.id
    version = aws_launch_template.this.latest_version
  }
  # a new AMI or user-data replaces the instance: there is only one, so egress stops until the new one has the ENI
  instance_refresh {
    strategy = "Rolling"
    preferences {
      min_healthy_percentage = 0
    }
  }
  depends_on = [aws_iam_role_policy.this]
}

output "eni_id" {
  value = aws_network_interface.static.id
}
output "public_ip" {
  description = "The EIP, if eip = true."
  value       = var.eip ? aws_eip.this[0].public_ip : null
}
output "security_group_id" {
  value = aws_security_group.this.id
}
output "role_name" {
  value = aws_iam_role.this.name
}
