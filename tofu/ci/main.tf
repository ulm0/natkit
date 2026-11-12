# What natkit's CI (.github/workflows/build.yml) needs in AWS: a GitHub OIDC role for the AMI import and the boot
# test, a small test VPC (IPv4 + IPv6: a public subnet, a private subnet with its own empty route table), and the
# NAT instances' test profile. `tofu output repo_variables` lists the repository variables to set.
# Not created here: the account's GitHub OIDC provider, the S3 import bucket and the vmimport role (reuse yagan's).
terraform {
  required_version = ">= 1.10"
  # bucket, key and region come from `init -backend-config=backend.tfvars` (gitignored)
  backend "s3" {
    use_lockfile = true
  }
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 6.0" }
  }
}

variable "name" {
  type    = string
  default = "natkit-ci"
}
variable "github_subject" {
  description = "OIDC subject pattern of the natkit repo, e.g. repo:<owner>@<owner id>/natkit@<repo id>:* (GitHub's immutable subject format) or repo:<owner>/natkit:*."
  type        = string
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
}

# ---- test network
resource "aws_vpc" "test" {
  cidr_block                       = var.cidr
  assign_generated_ipv6_cidr_block = true
  enable_dns_support               = true
  tags                             = local.tags
}
resource "aws_internet_gateway" "test" {
  vpc_id = aws_vpc.test.id
  tags   = local.tags
}
resource "aws_subnet" "public" {
  vpc_id            = aws_vpc.test.id
  availability_zone = data.aws_availability_zones.available.names[0]
  cidr_block        = cidrsubnet(var.cidr, 2, 0)
  ipv6_cidr_block   = cidrsubnet(aws_vpc.test.ipv6_cidr_block, 8, 0)
  tags              = { Name = "${var.name}-public" }
}
resource "aws_subnet" "private" {
  vpc_id            = aws_vpc.test.id
  availability_zone = data.aws_availability_zones.available.names[0]
  cidr_block        = cidrsubnet(var.cidr, 2, 1)
  ipv6_cidr_block   = cidrsubnet(aws_vpc.test.ipv6_cidr_block, 8, 1)
  tags              = { Name = "${var.name}-private" }
}
resource "aws_route_table" "public" {
  vpc_id = aws_vpc.test.id
  tags   = { Name = "${var.name}-public" }
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.test.id
  }
}
# no routes: the boot test adds 0.0.0.0/0 and 64:ff9b::/96 -> its ENI, and removes them
resource "aws_route_table" "private" {
  vpc_id = aws_vpc.test.id
  tags   = { Name = "${var.name}-private" }
  lifecycle {
    ignore_changes = [route]
  }
}
resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}
resource "aws_route_table_association" "private" {
  subnet_id      = aws_subnet.private.id
  route_table_id = aws_route_table.private.id
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
      { Effect = "Allow", Action = ["ec2:AssociateAddress", "ec2:AttachNetworkInterface", "ec2:ModifyNetworkInterfaceAttribute"], Resource = "*" },
      { Effect = "Allow", Action = "ssm:GetParameter", Resource = "${local.arn}:ssm:${local.region}:${local.account}:parameter/natkit/boot-test/*" },
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
        StringLike   = { "token.actions.githubusercontent.com:sub" = var.github_subject }
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
      { Sid = "ImportObjects", Effect = "Allow", Action = ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"], Resource = "${local.arn}:s3:::${var.bucket}/import/*" },
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
      { Sid = "TestParameters", Effect = "Allow", Action = ["ssm:PutParameter", "ssm:DeleteParameter"], Resource = "${local.arn}:ssm:${local.region}:${local.account}:parameter/natkit/boot-test/*" },
      { Sid = "PublicAmiParameters", Effect = "Allow", Action = "ssm:GetParameter", Resource = "${local.arn}:ssm:${local.region}::parameter/aws/service/ami-amazon-linux-latest/*" },
      { Sid = "AgentMetrics", Effect = "Allow", Action = "cloudwatch:GetMetricStatistics", Resource = "*" },
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
    NATKIT_TEST_PUBLIC_SUBNET    = aws_subnet.public.id
    NATKIT_TEST_PRIVATE_SUBNET   = aws_subnet.private.id
    NATKIT_TEST_INSTANCE_PROFILE = aws_iam_instance_profile.nat.name
  }
}
