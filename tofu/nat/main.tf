# A natkit NAT instance in fck-nat's HA layout: an Auto Scaling group of one, a static ENI the private route tables
# point at (the replacement instance takes it over), and optionally an EIP. User-data is natkit's YAML.
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
    aws = { source = "hashicorp/aws", version = ">= 6.68" } # connection_tracking_specification on aws_network_interface
  }
}

variable "vpc_id" {
  description = "The VPC."
  type        = string
}
variable "subnet_id" {
  description = "Public subnet (internet gateway route) for the instance and its static ENI."
  type        = string
}
variable "route_table_ids" {
  description = "Private route tables that get `0.0.0.0/0` (and `64:ff9b::/96` with `nat64`) to the static ENI."
  type        = list(string)
}
variable "ami_id" {
  description = "A natkit AMI of the instance type's architecture, from the same release as the module."
  type        = string
}
variable "name" {
  description = "Name and `natkit` tag of every resource; with `conntrack_alarm_actions`, the `Name` dimension of the metrics."
  type        = string
  default     = "natkit"
}
variable "instance_type" {
  description = "0.5 GiB of memory is enough (`t4g.nano`, `t3.nano`); the CloudWatch agent (`cwagent_cfg_param_name`) needs 1 GiB, and the module refuses a `*.nano` with it. See [instance size](https://natkit.ulm0.com/docs/start/getting-started/#instance-size)."
  type        = string
  default     = "t4g.micro"
}
variable "eip" {
  description = "Allocate an EIP and have the instance take it. Otherwise it egresses from its own public IP, which changes on replacement."
  type        = bool
  default     = true
}
variable "nat64" {
  description = "Route `64:ff9b::/96` to the ENI and give it an IPv6 address (the subnet needs an IPv6 CIDR). Pair with Route 53 Resolver DNS64: see [NAT64](https://natkit.ulm0.com/docs/how-it-works/nat64/)."
  type        = bool
  default     = false
}
variable "ipv6_cidr" {
  description = "The VPC's IPv6 CIDR, a default client with `nat64`."
  type        = string
  default     = null
}
variable "client_cidrs" {
  description = "Sources that may route through the NAT, IPv4 and IPv6, in canonical form. Enforced by the static ENI's security group (unless `untracked`) and by natkit's nftables ([`client_cidrs`](https://natkit.ulm0.com/docs/reference/configuration/)). Default: the VPC's IPv4 CIDRs, plus `ipv6_cidr`."
  type        = list(string)
  default     = null
  validation {
    condition     = var.client_cidrs == null || (length(coalesce(var.client_cidrs, [])) > 0 && alltrue([for c in coalesce(var.client_cidrs, []) : try(cidrsubnet(c, 0, 0) == lower(c), false)]))
    error_message = "client_cidrs: a non-empty list of CIDRs in canonical form: no host bits, no leading zeros (10.0.0.0/16, 2600:1f18::/56)."
  }
}
variable "untracked" {
  description = "Open the static ENI's security group to all traffic, so EC2 doesn't track the flows through it: about twice the connections before EC2's per-instance allowance runs out. `client_cidrs` is then enforced by natkit's nftables only. See [Performance](https://natkit.ulm0.com/docs/operate/performance/)."
  type        = bool
  default     = false
}
variable "tcp_established_timeout" {
  description = "EC2's idle timeout, in seconds (60 to 432000), for established TCP connections it tracks on both ENIs. Default: EC2's (432000, or 350 on Nitro v6 instances). 350 matches NAT Gateway and frees the allowance from abandoned connections sooner. Going back to `null` leaves the static ENI at its last value: set EC2's default explicitly instead. See [Performance](https://natkit.ulm0.com/docs/operate/performance/)."
  type        = number
  default     = null
  validation {
    condition     = var.tcp_established_timeout == null || (coalesce(var.tcp_established_timeout, 60) >= 60 && coalesce(var.tcp_established_timeout, 60) <= 432000)
    error_message = "tcp_established_timeout: 60 to 432000 seconds."
  }
}
variable "settings" {
  description = "natkit's tuning [user-data keys](https://natkit.ulm0.com/docs/reference/configuration/): `ip_local_port_range`, `nf_conntrack_max`, `nf_conntrack_buckets`, `nf_conntrack_tcp_timeout_established`, `tcp_keepalive_time`, `tcp_max_syn_backlog`. Any other key is refused at plan time."
  type        = map(any)
  default     = {}
  nullable    = false
  # natkit refuses a user-data key it doesn't know, at boot: a typo here would otherwise be a NAT that never comes up
  validation {
    condition = alltrue([for k in keys(var.settings) : contains([
      "ip_local_port_range", "nf_conntrack_max", "nf_conntrack_buckets", "nf_conntrack_tcp_timeout_established",
      "tcp_keepalive_time", "tcp_max_syn_backlog",
    ], k)])
    error_message = "settings: only ip_local_port_range, nf_conntrack_max, nf_conntrack_buckets, nf_conntrack_tcp_timeout_established, tcp_keepalive_time and tcp_max_syn_backlog. The other user-data keys come from this module's own variables."
  }
}
variable "cwagent_cfg_param_name" {
  description = "SSM parameter with a [CloudWatch agent](https://natkit.ulm0.com/docs/operate/monitoring/) JSON config: enables the agent. Needs 1 GiB of memory: a micro or larger."
  type        = string
  default     = null
}
variable "conntrack_alarm_actions" {
  description = "Alarm when EC2 drops packets because the instance's [connection-tracking allowance](https://natkit.ulm0.com/docs/operate/performance/) ran out, and when the NAT stops reporting, with these actions (SNS topic ARNs; `[]` for an alarm without any). natkit publishes the ENA driver's allowance counters itself ([`metrics`](https://natkit.ulm0.com/docs/operate/monitoring/#allowance-metrics)), without the CloudWatch agent. CloudWatch bills the two metrics, the requests and the alarm: see [Costs](https://natkit.ulm0.com/docs/operate/costs/)."
  type        = list(string)
  default     = null
}
variable "instance_profile" {
  description = "Name of an existing instance profile to use instead of the module's role, where IAM is managed elsewhere. Its role needs the [instance role](https://natkit.ulm0.com/docs/reference/instance-role/)'s permissions."
  type        = string
  default     = null
}
variable "console_password_param" {
  description = "SSM parameter with root's crypt hash (`openssl passwd -6`) for the EC2 Serial Console."
  type        = string
  default     = null
}
variable "tags" {
  description = "Extra tags for every resource."
  type        = map(string)
  default     = {}
}

data "aws_vpc" "this" {
  id = var.vpc_id
}
# its name, for the launch template's precondition
data "aws_ami" "this" {
  filter {
    name   = "image-id"
    values = [var.ami_id]
  }
}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  tags      = merge(var.tags, { Name = var.name, natkit = var.name })
  arn       = "arn:${data.aws_partition.current.partition}"
  ec2_arn   = "${local.arn}:ec2:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}"
  alarm     = var.conntrack_alarm_actions != null
  ssm_names = compact([var.cwagent_cfg_param_name, var.console_password_param])
  client_cidrs = var.client_cidrs != null ? var.client_cidrs : concat(
    [for a in data.aws_vpc.this.cidr_block_associations : a.cidr_block if a.state == "associated"],
    var.ipv6_cidr == null ? [] : [var.ipv6_cidr],
  )
  hook      = "natkit-ready" # aws_autoscaling_lifecycle_hook.ready, by name: the launch template can't depend on the group
  client_v4 = [for c in local.client_cidrs : c if !strcontains(c, ":")]
  client_v6 = [for c in local.client_cidrs : c if strcontains(c, ":")]
  user_data = merge(
    var.settings,
    { eni_id = aws_network_interface.static.id, client_cidrs = local.client_cidrs, lifecycle_hook = local.hook },
    var.eip ? { eip_id = aws_eip.this[0].allocation_id } : {},
    var.nat64 ? {} : { nat64 = false },
    local.alarm ? { metrics = var.name } : {},
    var.cwagent_cfg_param_name == null ? {} : { cwagent_enabled = true, cwagent_cfg_param_name = var.cwagent_cfg_param_name },
    var.console_password_param == null ? {} : { console_password = var.console_password_param },
  )
}

# the public ENI: replies to what the NAT opened, nothing new from anywhere
resource "aws_security_group" "public" {
  name_prefix = "${var.name}-public-"
  description = "natkit NAT instance, public ENI"
  vpc_id      = var.vpc_id
  tags        = local.tags

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

# the static ENI, the route tables' target: what the clients send to be forwarded arrives here. Only the clients,
# or (untracked) everything: EC2 tracks no flow whose security group admits all traffic in both directions, and
# natkit's nftables forwards only the clients' new connections either way.
resource "aws_security_group" "private" {
  name_prefix = "${var.name}-"
  description = "natkit NAT instance"
  vpc_id      = var.vpc_id
  tags        = local.tags

  ingress {
    description      = var.untracked ? "all (untracked; natkit forwards only client_cidrs)" : "clients"
    protocol         = "-1"
    from_port        = 0
    to_port          = 0
    cidr_blocks      = var.untracked ? ["0.0.0.0/0"] : local.client_v4
    ipv6_cidr_blocks = var.untracked ? ["::/0"] : local.client_v6
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
moved {
  from = aws_security_group.this
  to   = aws_security_group.private
}

resource "aws_network_interface" "static" {
  description        = "${var.name} static ENI (route target)"
  subnet_id          = var.subnet_id
  security_groups    = [aws_security_group.private.id]
  source_dest_check  = false
  ipv6_address_count = var.nat64 ? 1 : 0
  tags               = local.tags

  dynamic "connection_tracking_specification" {
    for_each = var.tcp_established_timeout == null ? [] : [var.tcp_established_timeout]
    content {
      tcp_established_timeout = connection_tracking_specification.value
    }
  }
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
  count       = var.instance_profile == null ? 1 : 0
  name_prefix = "${var.name}-"
  tags        = local.tags
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "sts:AssumeRole", Principal = { Service = "ec2.amazonaws.com" } }]
  })
}
resource "aws_iam_role_policy" "this" {
  count = var.instance_profile == null ? 1 : 0
  name  = "natkit"
  role  = aws_iam_role.this[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat(
      [{
        Sid       = "OwnInterfaces"
        Effect    = "Allow"
        Action    = ["ec2:AttachNetworkInterface", "ec2:DetachNetworkInterface", "ec2:ModifyNetworkInterfaceAttribute", "ec2:AssociateAddress"]
        Resource  = ["${local.ec2_arn}:network-interface/*", "${local.ec2_arn}:instance/*"]
        Condition = { StringEquals = { "aws:ResourceTag/natkit" = var.name } }
        }, {
        # the static ENI's attachment, to take it over from the instance being replaced, and the instance's own
        # group and lifecycle state; Describe* takes no resource
        Sid      = "Find"
        Effect   = "Allow"
        Action   = ["ec2:DescribeNetworkInterfaces", "autoscaling:DescribeAutoScalingInstances"]
        Resource = "*"
        }, {
        # the group's launch lifecycle hook (aws_autoscaling_lifecycle_hook.ready), which natkit completes when ready.
        # The group's name is only known as a prefix here; its natkit tag makes it this module's group and no other
        Sid       = "ReadyHook"
        Effect    = "Allow"
        Action    = ["autoscaling:RecordLifecycleActionHeartbeat", "autoscaling:CompleteLifecycleAction"]
        Resource  = "${local.arn}:autoscaling:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:autoScalingGroup:*:autoScalingGroupName/${var.name}-*"
        Condition = { StringEquals = { "aws:ResourceTag/natkit" = var.name } }
      }],
      var.eip ? [{
        Sid      = "OwnEip"
        Effect   = "Allow"
        Action   = "ec2:AssociateAddress"
        Resource = "${local.ec2_arn}:elastic-ip/${aws_eip.this[0].allocation_id}"
      }] : [],
      local.alarm ? [{
        # natkit-metrics: the allowance counters, to this module's namespace only
        Sid       = "AllowanceMetrics"
        Effect    = "Allow"
        Action    = "cloudwatch:PutMetricData"
        Resource  = "*"
        Condition = { StringEquals = { "cloudwatch:namespace" = "natkit" } }
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
  count      = var.instance_profile == null && var.cwagent_cfg_param_name != null ? 1 : 0
  role       = aws_iam_role.this[0].name
  policy_arn = "${local.arn}:iam::aws:policy/CloudWatchAgentServerPolicy"
}
resource "aws_iam_instance_profile" "this" {
  count       = var.instance_profile == null ? 1 : 0
  name_prefix = "${var.name}-"
  role        = aws_iam_role.this[0].name
  tags        = local.tags
}

resource "aws_launch_template" "this" {
  name_prefix   = "${var.name}-"
  image_id      = var.ami_id
  instance_type = var.instance_type
  user_data     = base64encode(yamlencode(local.user_data))
  tags          = local.tags

  iam_instance_profile {
    name = var.instance_profile != null ? var.instance_profile : aws_iam_instance_profile.this[0].name
  }
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }
  # a public IP at launch: natkit-init reaches the EC2 API with it before it has the EIP
  network_interfaces {
    associate_public_ip_address = true
    security_groups             = [aws_security_group.public.id]
    delete_on_termination       = true
    ipv6_address_count          = var.nat64 ? 1 : 0

    dynamic "connection_tracking_specification" {
      for_each = var.tcp_established_timeout == null ? [] : [var.tcp_established_timeout]
      content {
        tcp_established_timeout = connection_tracking_specification.value
      }
    }
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
  # natkit v0.1.3 and earlier stop at boot on lifecycle_hook, an unknown key to them (v0.1.1 and earlier on
  # client_cidrs too): the replacement would never be put in service, and the rollout would never finish
  lifecycle {
    precondition {
      condition     = !can(regex("^natkit-v0\\.1\\.[0-3]-(arm64|amd64)$", data.aws_ami.this.name)) # the releases' names only
      error_message = "${data.aws_ami.this.name} doesn't know lifecycle_hook, which this module sets: use natkit v0.1.5 or later, the module's own release."
    }
    # the agent's image is pulled into memory: on 0.5 GiB it doesn't fit, and the agent would never start
    precondition {
      condition     = var.cwagent_cfg_param_name == null || !endswith(var.instance_type, ".nano")
      error_message = "The CloudWatch agent (cwagent_cfg_param_name) needs 1 GiB of memory: use a micro or larger instance type."
    }
    # the metrics' dimension is the module's name, and natkit refuses a metrics value outside these characters at
    # boot: here it is a plan that fails, not a NAT that never comes up
    precondition {
      condition     = !local.alarm || can(regex("^[A-Za-z0-9._-]{1,255}$", var.name))
      error_message = "conntrack_alarm_actions publishes the metrics under the module's name: only letters, digits, '.', '_' and '-' in name."
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
  # A replacement (a new AMI or user-data, an unhealthy instance) launches before the old instance terminates, and
  # takes the ENI over once its own setup is done: egress stops for the handover only. The old instance goes once
  # the new one is in service (aws_autoscaling_lifecycle_hook.ready) and the warmup has passed.
  instance_maintenance_policy {
    min_healthy_percentage = 100
    max_healthy_percentage = 200
  }
  default_instance_warmup = 90
  instance_refresh {
    strategy = "Rolling"
  }
  # the instances get their tags from the launch template; this one is the group's own, for the role's ReadyHook
  tag {
    key                 = "natkit"
    value               = var.name
    propagate_at_launch = false
  }
  depends_on = [aws_iam_role_policy.this]
}

# A new instance is in service when natkit says so, not when EC2 calls it running: the hook holds it in Pending:Wait
# until natkit-init completes it, with its setup done and the ENI taken (the EIP follows). One that never does (a
# setting natkit refuses, an image that doesn't boot) is terminated when the hook times out and the group tries again;
# meanwhile the old instance, still in service, keeps routing. Without the hook the group would terminate the old
# instance after the warmup, whatever became of the new one.
# A resource of its own, not the group's initial_lifecycle_hook: that one only takes at the group's creation, and
# groups made by earlier versions of this module need the hook too. The first instance of a new group launches
# before the hook exists: there is nothing for it to replace.
resource "aws_autoscaling_lifecycle_hook" "ready" {
  name                   = local.hook
  autoscaling_group_name = aws_autoscaling_group.this.name
  lifecycle_transition   = "autoscaling:EC2_INSTANCE_LAUNCHING"
  heartbeat_timeout      = 300
  default_result         = "ABANDON"
}

# conntrack_alarm_actions: natkit publishes the ENA driver's allowance counters under the module's name (the
# instance ID changes with every replacement): the packets EC2 dropped since its last report, so their sum over a
# minute is what the alarm watches. A report is a new connection too: while the allowance is exhausted EC2 drops
# that as well and the alarm has no data. Missing data is therefore reported too (insufficient_data_actions), or the
# alarm would stay silent for as long as the drops last; so is a NAT that is down.
resource "aws_cloudwatch_metric_alarm" "conntrack" {
  count                     = local.alarm ? 1 : 0
  alarm_name                = "${var.name}-conntrack-allowance-exceeded"
  alarm_description         = "EC2 dropped packets on the ${var.name} NAT instance: its connection-tracking allowance ran out. Untrack the static ENI, shorten the idle timeout or use a larger instance type. Without data the instance isn't reporting: it is down, or the allowance is exhausted and its reports are dropped as well."
  namespace                 = "natkit"
  metric_name               = "conntrack_allowance_exceeded"
  dimensions                = { Name = var.name }
  statistic                 = "Sum"
  period                    = 60
  comparison_operator       = "GreaterThanThreshold"
  threshold                 = 0
  evaluation_periods        = 1
  treat_missing_data        = "missing"
  alarm_actions             = var.conntrack_alarm_actions
  insufficient_data_actions = var.conntrack_alarm_actions
  tags                      = local.tags
}

output "eni_id" {
  description = "The static ENI: the private route tables' target."
  value       = aws_network_interface.static.id
}
output "public_ip" {
  description = "The EIP, with `eip = true`."
  value       = var.eip ? aws_eip.this[0].public_ip : null
}
output "security_group_id" {
  description = "The static ENI's: what the clients' traffic must pass."
  value       = aws_security_group.private.id
}
output "public_security_group_id" {
  description = "The instance's primary (public) ENI's."
  value       = aws_security_group.public.id
}
output "autoscaling_group_name" {
  description = "The Auto Scaling group of one."
  value       = aws_autoscaling_group.this.name
}
output "conntrack_alarm_name" {
  description = "The alarm, with `conntrack_alarm_actions`."
  value       = one(aws_cloudwatch_metric_alarm.conntrack[*].alarm_name)
}
output "role_name" {
  description = "The instance role this module created; null with `instance_profile`."
  value       = one(aws_iam_role.this[*].name)
}
