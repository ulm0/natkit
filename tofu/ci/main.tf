# What natkit's CI (.github/workflows/build.yml) needs in AWS: a GitHub OIDC role for the AMI import, the boot test
# and the module test, a small test VPC (IPv4 + IPv6: a public subnet, a private subnet with its own empty route table), and the
# NAT instances' test profile. `tofu output repo_variables` lists the repository variables to set.
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
  # the boot test's agent config, and the one tofu/nat writes for the module test (its name is "natkit-module-test")
  test_params = ["/natkit/boot-test/*", "/natkit/natkit-module-test/*"]
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

# ---- the test NAT instances' role (they get EIPs and ENIs the test creates, so no resource scoping here)
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
      { Effect = "Allow", Action = ["ec2:AssociateAddress", "ec2:AttachNetworkInterface", "ec2:DetachNetworkInterface", "ec2:DescribeNetworkInterfaces", "ec2:ModifyNetworkInterfaceAttribute"], Resource = "*" },
      { Effect = "Allow", Action = "ssm:GetParameter", Resource = [for p in local.test_params : "${local.ssm_arn}:parameter${p}"] },
      # the module test's group: a NAT whose setup failed reports itself unhealthy
      { Effect = "Allow", Action = "autoscaling:SetInstanceHealth", Resource = "${local.arn}:autoscaling:${local.region}:${local.account}:autoScalingGroup:*:autoScalingGroupName/natkit-module-test-*" },
    ]
  })
}
resource "aws_iam_role_policy_attachment" "nat_cwagent" {
  role       = aws_iam_role.nat.name
  policy_arn = "${local.arn}:iam::aws:policy/CloudWatchAgentServerPolicy"
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
      # Describe* calls take no resource; the rest (AMI import, the boot test's instances, EIP, ENI and routes) are
      # created by the workflow, so they can't be named ahead
      {
        Sid    = "AmiAndBootTest"
        Effect = "Allow"
        Action = [
          "ec2:Describe*", "ec2:GetConsoleOutput", "ec2:ImportSnapshot", "ec2:RegisterImage", "ec2:CreateTags",
          "ec2:RunInstances", "ec2:TerminateInstances", "ec2:AllocateAddress", "ec2:ReleaseAddress",
          "ec2:CreateNetworkInterface", "ec2:DeleteNetworkInterface", "ec2:ModifyNetworkInterfaceAttribute",
          "ec2:CreateRoute", "ec2:ReplaceRoute", "ec2:DeleteRoute",
        ]
        Resource = "*"
      },
      # boot-cleanup prunes per-commit AMIs; only images/snapshots scripts/ami.sh tagged
      {
        Sid       = "PruneAmis"
        Effect    = "Allow"
        Action    = ["ec2:DeregisterImage", "ec2:DeleteSnapshot"]
        Resource  = ["${local.arn}:ec2:${local.region}::image/*", "${local.arn}:ec2:${local.region}::snapshot/*"]
        Condition = { StringLike = { "aws:ResourceTag/natkit.io/release" = "natkit-*" } }
      },
      {
        Sid      = "TestParameters"
        Effect   = "Allow"
        Action   = ["ssm:PutParameter", "ssm:DeleteParameter", "ssm:GetParameter", "ssm:GetParameters", "ssm:ListTagsForResource", "ssm:AddTagsToResource"]
        Resource = [for p in local.test_params : "${local.ssm_arn}:parameter${p}"]
      },
      # The module test applies tofu/nat (test/module): security groups and a launch template (created by the
      # workflow, so not named ahead), the Auto Scaling group and the alarm under the module's name.
      {
        Sid    = "ModuleTestEc2"
        Effect = "Allow"
        Action = [
          "ec2:CreateSecurityGroup", "ec2:DeleteSecurityGroup", "ec2:AuthorizeSecurityGroupIngress",
          "ec2:AuthorizeSecurityGroupEgress", "ec2:RevokeSecurityGroupIngress", "ec2:RevokeSecurityGroupEgress",
          "ec2:CreateLaunchTemplate", "ec2:CreateLaunchTemplateVersion", "ec2:ModifyLaunchTemplate",
          "ec2:DeleteLaunchTemplate", "ec2:DetachNetworkInterface", "ec2:DisassociateAddress",
        ]
        Resource = "*"
      },
      { Sid = "ModuleTestDescribe", Effect = "Allow", Action = ["autoscaling:Describe*", "cloudwatch:DescribeAlarms", "ssm:DescribeParameters"], Resource = "*" },
      {
        Sid    = "ModuleTestGroup"
        Effect = "Allow"
        Action = [
          "autoscaling:CreateAutoScalingGroup", "autoscaling:UpdateAutoScalingGroup", "autoscaling:DeleteAutoScalingGroup",
          "autoscaling:StartInstanceRefresh", "autoscaling:CancelInstanceRefresh", "autoscaling:CreateOrUpdateTags",
          "autoscaling:DeleteTags", "autoscaling:SetInstanceProtection",
        ]
        Resource = "${local.arn}:autoscaling:${local.region}:${local.account}:autoScalingGroup:*:autoScalingGroupName/natkit-module-test-*"
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
