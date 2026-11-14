# What natkit's CI (.github/workflows/build.yml) needs in AWS: a GitHub OIDC role for the AMI import, the boot test
# and the module test, a small test VPC (IPv4 + IPv6: a public subnet, a private subnet with its own empty route table), and the
# NAT instances' test profile. `tofu output repo_variables` lists the repository variables to set.
# Both roles reach only the test VPC and what a test tagged: the account may hold other things (a production NAT,
# other VPCs), and a test script with a bug, or a tool in the job that was tampered with, must not get to them.
# Not created here: the account's GitHub OIDC provider, the S3 import bucket and the vmimport role (reuse yagan's).
terraform {
  required_version = ">= 1.10"
  # bucket, key and region come from `init -backend-config=backend.tfvars` (gitignored)
  backend "s3" {
    use_lockfile = true
  }
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 6.28" }
  }
}

variable "name" {
  type    = string
  default = "natkit-ci"
}
variable "github_subjects" {
  description = "OIDC subjects allowed to assume the CI role: the natkit repo's main branch and release tags, e.g. [\"repo:<owner>/natkit:ref:refs/heads/main\", \"repo:<owner>/natkit:ref:refs/tags/v*\"] (or GitHub's immutable form, repo:<owner>@<owner id>/natkit@<repo id>:...). Not repo:...:*: any branch that can run a workflow could then take the role."
  type        = list(string)
  validation {
    condition     = alltrue([for s in var.github_subjects : !endswith(s, ":*")])
    error_message = "Name the refs (…:ref:refs/heads/main, …:ref:refs/tags/v*), not every ref of the repo (…:*)."
  }
}
variable "oidc_provider_arn" {
  description = "The account's token.actions.githubusercontent.com OIDC provider."
  type        = string
}
variable "bucket" {
  description = "Existing S3 bucket for image imports (objects under import/)."
  type        = string
}
variable "cidr" {
  type    = string
  default = "10.99.0.0/24"
}

data "aws_region" "current" {}
data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}
data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  tags    = { Name = var.name }
  arn     = "arn:${data.aws_partition.current.partition}"
  account = data.aws_caller_identity.current.account_id
  region  = data.aws_region.current.region
  ec2_arn = "${local.arn}:ec2:${local.region}:${local.account}"
  ssm_arn = "${local.arn}:ssm:${local.region}:${local.account}"
  vpc_arn = "${local.ec2_arn}:vpc/${module.vpc.vpc_id}"
  # the module test's Auto Scaling group (tofu/nat names it <name>-<suffix>)
  test_group = "${local.arn}:autoscaling:${local.region}:${local.account}:autoScalingGroup:*:autoScalingGroupName/natkit-module-test-*"
  # what natkit-init calls on interfaces and instances
  nat_actions = ["ec2:AssociateAddress", "ec2:AttachNetworkInterface", "ec2:DetachNetworkInterface", "ec2:ModifyNetworkInterfaceAttribute"]
  # the boot test's agent config
  test_params = ["/natkit/boot-test/*"]
}

# ---- test network: a public subnet and a private one with its own route table, IPv4 + IPv6, one AZ
module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "6.7.3"

  name            = var.name
  cidr            = var.cidr
  azs             = [data.aws_availability_zones.available.names[0]]
  public_subnets  = [cidrsubnet(var.cidr, 2, 0)]
  private_subnets = [cidrsubnet(var.cidr, 2, 1)]

  enable_ipv6                  = true
  public_subnet_ipv6_prefixes  = [0]
  private_subnet_ipv6_prefixes = [1]

  # the private route table stays empty: boot-test.sh adds 0.0.0.0/0 and 64:ff9b::/96 -> its ENI and removes them, and
  # any other egress (NAT gateway, egress-only IGW) would let the client's checks pass without natkit
  enable_nat_gateway     = false
  create_egress_only_igw = false
  # the test instances launch in the default security group, which must keep its allow-within-group rules
  manage_default_security_group = false

  tags = local.tags
}

# ---- the test NAT instances' role: what natkit-init and natkit-metrics call, only on the test VPC's interfaces, on
# instances with this profile and on Elastic IPs a test tagged. The CI role can launch an instance with this
# profile and its own user-data, so what this role reaches, CI reaches.
resource "aws_iam_role" "nat" {
  name = "${var.name}-nat"
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "sts:AssumeRole", Principal = { Service = "ec2.amazonaws.com" } }]
  })
}
resource "aws_iam_role_policy" "nat" {
  name = "natkit"
  role = aws_iam_role.nat.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      # Describe* calls take no resource (DescribeTags, DescribeVolumes: the CloudWatch agent's)
      { Sid = "Describe", Effect = "Allow", Action = ["ec2:DescribeNetworkInterfaces", "ec2:DescribeTags", "ec2:DescribeVolumes", "autoscaling:DescribeAutoScalingInstances"], Resource = "*" },
      { Sid = "TestVpcInterfaces", Effect = "Allow", Action = local.nat_actions, Resource = "${local.ec2_arn}:network-interface/*", Condition = { ArnEquals = { "ec2:Vpc" = local.vpc_arn } } },
      { Sid = "TestInstances", Effect = "Allow", Action = local.nat_actions, Resource = "${local.ec2_arn}:instance/*", Condition = { ArnEquals = { "ec2:InstanceProfile" = aws_iam_instance_profile.nat.arn } } },
      { Sid = "BootTestAddresses", Effect = "Allow", Action = "ec2:AssociateAddress", Resource = "${local.ec2_arn}:elastic-ip/*", Condition = { StringLike = { "aws:ResourceTag/natkit.io/boot-test" = "*" } } },
      { Sid = "ModuleTestAddresses", Effect = "Allow", Action = "ec2:AssociateAddress", Resource = "${local.ec2_arn}:elastic-ip/*", Condition = { StringLike = { "aws:ResourceTag/natkit.io/module-test" = "*" } } },
      { Sid = "TestParameters", Effect = "Allow", Action = "ssm:GetParameter", Resource = [for p in local.test_params : "${local.ssm_arn}:parameter${p}"] },
      # the module test's group: its instances complete the launch lifecycle hook when they're ready
      { Sid = "ReadyHook", Effect = "Allow", Action = ["autoscaling:RecordLifecycleActionHeartbeat", "autoscaling:CompleteLifecycleAction"], Resource = local.test_group },
      # natkit-metrics (namespace natkit) and the boot test's CloudWatch agent (natkit-boot-test). Not the managed
      # CloudWatchAgentServerPolicy: it also writes to every log group and sets their retention
      { Sid = "Metrics", Effect = "Allow", Action = "cloudwatch:PutMetricData", Resource = "*", Condition = { StringEquals = { "cloudwatch:namespace" = ["natkit", "natkit-boot-test"] } } },
    ]
  })
}
resource "aws_iam_instance_profile" "nat" {
  name = "${var.name}-nat"
  role = aws_iam_role.nat.name
}

# ---- the GitHub Actions role
resource "aws_iam_role" "github" {
  name = "${var.name}-github"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRoleWithWebIdentity"
      Principal = { Federated = var.oidc_provider_arn }
      Condition = {
        StringEquals = { "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com" }
        StringLike   = { "token.actions.githubusercontent.com:sub" = var.github_subjects }
      }
    }]
  })
}
resource "aws_iam_role_policy" "github" {
  name = "natkit-ci"
  role = aws_iam_role.github.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      # import/: the disk images; module-test/: the module test's state
      { Sid = "ImportObjects", Effect = "Allow", Action = ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"], Resource = ["${local.arn}:s3:::${var.bucket}/import/*", "${local.arn}:s3:::${var.bucket}/module-test/*"] },
      { Sid = "ImportBucket", Effect = "Allow", Action = ["s3:ListBucket", "s3:GetBucketLocation"], Resource = "${local.arn}:s3:::${var.bucket}" },
      # Describe* calls take no resource
      { Sid = "Describe", Effect = "Allow", Action = ["ec2:Describe*", "autoscaling:Describe*", "cloudwatch:DescribeAlarms"], Resource = "*" },
      # scripts/ami.sh. The import's request carries no tags and can't tag its snapshot, so the script tags it once it
      # exists (boot-cleanup prunes by natkit.io/release), and registers the image from a snapshot with that tag
      { Sid = "Import", Effect = "Allow", Action = "ec2:ImportSnapshot", Resource = "*" },
      {
        Sid       = "TagSnapshot"
        Effect    = "Allow"
        Action    = "ec2:CreateTags"
        Resource  = "${local.arn}:ec2:${local.region}::snapshot/*"
        Condition = { StringLike = { "aws:RequestTag/natkit.io/release" = "natkit-*" }, "ForAllValues:StringEquals" = { "aws:TagKeys" = ["Name", "natkit.io/release"] } }
      },
      { Sid = "RegisterAmi", Effect = "Allow", Action = "ec2:RegisterImage", Resource = "${local.arn}:ec2:${local.region}::image/*", Condition = { StringLike = { "aws:RequestTag/natkit.io/release" = "natkit-*" } } },
      { Sid = "RegisterFrom", Effect = "Allow", Action = "ec2:RegisterImage", Resource = "${local.arn}:ec2:${local.region}::snapshot/*", Condition = { StringLike = { "aws:ResourceTag/natkit.io/release" = "natkit-*" } } },
      # every other tag is set by the call that creates the resource: nothing that exists can be tagged into the
      # statements below
      {
        Sid       = "TagOnCreate"
        Effect    = "Allow"
        Action    = "ec2:CreateTags"
        Resource  = "*"
        Condition = { StringEquals = { "ec2:CreateAction" = ["RegisterImage", "AllocateAddress", "CreateNetworkInterface", "RunInstances", "CreateSecurityGroup", "CreateLaunchTemplate"] } }
      },
      # what the tests create carries their tag (boot-test.sh's TAGS; test/module's tags, through tofu/nat) ...
      {
        Sid       = "BootTestCreate"
        Effect    = "Allow"
        Action    = ["ec2:AllocateAddress", "ec2:CreateNetworkInterface"]
        Resource  = ["${local.ec2_arn}:elastic-ip/*", "${local.ec2_arn}:network-interface/*"]
        Condition = { StringLike = { "aws:RequestTag/natkit.io/boot-test" = "*" } }
      },
      { Sid = "BootTestLaunch", Effect = "Allow", Action = "ec2:RunInstances", Resource = "${local.ec2_arn}:instance/*", Condition = { StringLike = { "aws:RequestTag/natkit.io/boot-test" = "*" } } },
      {
        Sid       = "ModuleTestCreate"
        Effect    = "Allow"
        Action    = ["ec2:AllocateAddress", "ec2:CreateNetworkInterface", "ec2:CreateSecurityGroup", "ec2:CreateLaunchTemplate"]
        Resource  = ["${local.ec2_arn}:elastic-ip/*", "${local.ec2_arn}:network-interface/*", "${local.ec2_arn}:security-group/*", "${local.ec2_arn}:launch-template/*"]
        Condition = { StringLike = { "aws:RequestTag/natkit.io/module-test" = "*" } }
      },
      { Sid = "ModuleTestLaunch", Effect = "Allow", Action = "ec2:RunInstances", Resource = "${local.ec2_arn}:instance/*", Condition = { StringLike = { "aws:RequestTag/natkit.io/module-test" = "*" } } },
      # ... and what a create names besides the thing it makes: a launch's AMI and volume, a security group's VPC
      { Sid = "CreateUses", Effect = "Allow", Action = ["ec2:RunInstances", "ec2:CreateSecurityGroup"], Resource = ["${local.arn}:ec2:${local.region}::image/*", "${local.ec2_arn}:volume/*", local.vpc_arn] },
      # whatever has a VPC (subnets, ENIs, security groups, route tables): the test VPC's only. ec2:Vpc is not a key
      # of instances, EIPs or launch templates: those need a test's tag, in the two statements after this one
      {
        Sid    = "TestVpc"
        Effect = "Allow"
        Action = [
          "ec2:RunInstances", "ec2:CreateNetworkInterface", "ec2:ModifyNetworkInterfaceAttribute", "ec2:DetachNetworkInterface",
          "ec2:DeleteNetworkInterface", "ec2:DisassociateAddress", "ec2:CreateRoute", "ec2:ReplaceRoute", "ec2:DeleteRoute",
          "ec2:DeleteSecurityGroup", "ec2:AuthorizeSecurityGroupIngress", "ec2:AuthorizeSecurityGroupEgress",
          "ec2:RevokeSecurityGroupIngress", "ec2:RevokeSecurityGroupEgress",
        ]
        Resource  = ["${local.ec2_arn}:subnet/*", "${local.ec2_arn}:network-interface/*", "${local.ec2_arn}:security-group/*", "${local.ec2_arn}:route-table/*"]
        Condition = { ArnEquals = { "ec2:Vpc" = local.vpc_arn } }
      },
      {
        Sid       = "BootTestTagged"
        Effect    = "Allow"
        Action    = ["ec2:GetConsoleOutput", "ec2:TerminateInstances", "ec2:ReleaseAddress"]
        Resource  = ["${local.ec2_arn}:instance/*", "${local.ec2_arn}:elastic-ip/*"]
        Condition = { StringLike = { "aws:ResourceTag/natkit.io/boot-test" = "*" } }
      },
      {
        Sid    = "ModuleTestTagged"
        Effect = "Allow"
        Action = [
          "ec2:GetConsoleOutput", "ec2:TerminateInstances", "ec2:DetachNetworkInterface", "ec2:ModifyNetworkInterfaceAttribute",
          "ec2:DisassociateAddress", "ec2:ReleaseAddress", "ec2:RunInstances", "ec2:CreateLaunchTemplateVersion", "ec2:DeleteLaunchTemplate",
        ]
        Resource  = ["${local.ec2_arn}:instance/*", "${local.ec2_arn}:elastic-ip/*", "${local.ec2_arn}:launch-template/*"]
        Condition = { StringLike = { "aws:ResourceTag/natkit.io/module-test" = "*" } }
      },
      # boot-cleanup prunes per-commit AMIs; only images/snapshots scripts/ami.sh tagged
      {
        Sid       = "PruneAmis"
        Effect    = "Allow"
        Action    = ["ec2:DeregisterImage", "ec2:DeleteSnapshot"]
        Resource  = ["${local.arn}:ec2:${local.region}::image/*", "${local.arn}:ec2:${local.region}::snapshot/*"]
        Condition = { StringLike = { "aws:ResourceTag/natkit.io/release" = "natkit-*" } }
      },
      { Sid = "TestParameters", Effect = "Allow", Action = ["ssm:PutParameter", "ssm:DeleteParameter"], Resource = [for p in local.test_params : "${local.ssm_arn}:parameter${p}"] },
      # The module test's Auto Scaling group and alarm, under the module's name. The group always names a version of
      # its launch template: with $Latest or $Default, Auto Scaling would launch later versions without checking
      # this role's permissions again.
      { Sid = "NumberedTemplateVersion", Effect = "Deny", Action = ["autoscaling:CreateAutoScalingGroup", "autoscaling:UpdateAutoScalingGroup"], Resource = "*", Condition = { Bool = { "autoscaling:LaunchTemplateVersionSpecified" = "false" } } },
      {
        Sid    = "ModuleTestGroup"
        Effect = "Allow"
        Action = [
          "autoscaling:CreateAutoScalingGroup", "autoscaling:UpdateAutoScalingGroup", "autoscaling:DeleteAutoScalingGroup",
          "autoscaling:StartInstanceRefresh", "autoscaling:CancelInstanceRefresh", "autoscaling:CreateOrUpdateTags",
          "autoscaling:DeleteTags", "autoscaling:SetInstanceProtection", "autoscaling:PutLifecycleHook",
          "autoscaling:DeleteLifecycleHook",
        ]
        Resource = local.test_group
      },
      {
        Sid      = "ModuleTestAlarm"
        Effect   = "Allow"
        Action   = ["cloudwatch:PutMetricAlarm", "cloudwatch:DeleteAlarms", "cloudwatch:ListTagsForResource", "cloudwatch:TagResource"]
        Resource = "${local.arn}:cloudwatch:${local.region}:${local.account}:alarm:natkit-module-test-*"
      },
      { Sid = "PublicAmiParameters", Effect = "Allow", Action = "ssm:GetParameter", Resource = "${local.arn}:ssm:${local.region}::parameter/aws/service/ami-amazon-linux-latest/*" },
      { Sid = "AgentMetrics", Effect = "Allow", Action = "cloudwatch:GetMetricStatistics", Resource = "*" },
      # ImportSnapshot passes the vmimport service role (created outside this module) to VM Import/Export; no
      # iam:PassedToService condition: VM Import calls on the caller's behalf and some condition keys don't survive
      { Sid = "PassVmImportRole", Effect = "Allow", Action = "iam:PassRole", Resource = "${local.arn}:iam::${local.account}:role/vmimport" },
      # only the test profile's role, only to EC2
      {
        Sid       = "PassTestRole"
        Effect    = "Allow"
        Action    = "iam:PassRole"
        Resource  = aws_iam_role.nat.arn
        Condition = { StringEquals = { "iam:PassedToService" = "ec2.amazonaws.com" } }
      },
    ]
  })
}

output "repo_variables" {
  description = "Set these as GitHub repository variables (gh variable set NAME -b VALUE -R <owner>/natkit); AWS_ROLE_ARN as a secret (gh secret set)."
  value = {
    AWS_ROLE_ARN                 = aws_iam_role.github.arn
    AWS_REGION                   = local.region
    NATKIT_BUCKET                = var.bucket
    NATKIT_TEST_PUBLIC_SUBNET    = module.vpc.public_subnets[0]
    NATKIT_TEST_PRIVATE_SUBNET   = module.vpc.private_subnets[0]
    NATKIT_TEST_INSTANCE_PROFILE = aws_iam_instance_profile.nat.name
  }
}
