# What a fresh AWS account needs before tofu/ci can be applied, and before CI can import an image: the account's GitHub OIDC
# provider, the S3 bucket (image imports under import/, the module test's state under module-test/, and tofu/ci's own state), and
# the vmimport service role that VM Import/Export assumes to read the image from that bucket, and the Auto Scaling service-linked role. Apply this stack once, by hand,
# with administrator credentials; its own state is a local file (terraform.tfstate here, gitignored): it makes the bucket every
# other stack keeps its state in, so it cannot keep its own there. Keep that file (or import the resources again).
# An account that already has the OIDC provider or the vmimport role (another project's) imports them instead of creating them:
#   tofu import aws_iam_openid_connect_provider.github arn:aws:iam::<account>:oidc-provider/token.actions.githubusercontent.com
#   tofu import aws_iam_role.vmimport vmimport
# The region is the provider's usual one (AWS_REGION): the bucket must be in the region the AMIs are imported in.
terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 6.28" }
  }
}

variable "name" {
  description = "Prefix of what this stack names."
  type        = string
  default     = "natkit"
}
variable "bucket" {
  description = "Name of the S3 bucket (globally unique). Default: <name>-ci-<account id>."
  type        = string
  default     = null
}
variable "import_expiry_days" {
  description = "Days an image under import/ is kept. CI deletes its own; this removes what a failed run left behind."
  type        = number
  default     = 14
}

data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}
data "aws_region" "current" {}

locals {
  account = data.aws_caller_identity.current.account_id
  arn     = "arn:${data.aws_partition.current.partition}"
  bucket  = coalesce(var.bucket, "${var.name}-ci-${local.account}")
  tags    = { Name = "${var.name}-ci" }
}

# ---- GitHub Actions' OIDC provider. One per account and URL. AWS validates GitHub's certificate against its own trusted
# CAs, so no thumbprint is given; only the audience tofu/ci's role asks for is allowed.
resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
  tags           = local.tags
}

# ---- the bucket: private, versioned, encrypted, TLS only. tofu/ci's state is in it, so it is not destroyed by accident.
resource "aws_s3_bucket" "this" {
  bucket = local.bucket
  tags   = local.tags

  lifecycle {
    prevent_destroy = true
  }
}
resource "aws_s3_bucket_public_access_block" "this" {
  bucket                  = aws_s3_bucket.this.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_ownership_controls" "this" {
  bucket = aws_s3_bucket.this.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}
resource "aws_s3_bucket_versioning" "this" {
  bucket = aws_s3_bucket.this.id

  versioning_configuration {
    status = "Enabled"
  }
}
resource "aws_s3_bucket_server_side_encryption_configuration" "this" {
  bucket = aws_s3_bucket.this.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}
resource "aws_s3_bucket_policy" "tls_only" {
  bucket = aws_s3_bucket.this.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "TlsOnly"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.this.arn, "${aws_s3_bucket.this.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
  depends_on = [aws_s3_bucket_public_access_block.this]
}
# the images CI uploads are large and CI deletes them; a failed run's stay at most import_expiry_days. Old versions of the
# state and of anything else are kept for 30 days (and the last five), so a bad apply can be rolled back.
resource "aws_s3_bucket_lifecycle_configuration" "this" {
  bucket = aws_s3_bucket.this.id

  rule {
    id     = "import-images"
    status = "Enabled"

    filter {
      prefix = "import/"
    }
    expiration {
      days = var.import_expiry_days
    }
  }
  rule {
    id     = "old-versions"
    status = "Enabled"

    filter {}
    # the delete marker that expiring an import/ object leaves, once no version is left behind it
    expiration {
      expired_object_delete_marker = true
    }
    noncurrent_version_expiration {
      noncurrent_days           = 30
      newer_noncurrent_versions = 5
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
  depends_on = [aws_s3_bucket_versioning.this]
}

# ---- the vmimport service role (https://docs.aws.amazon.com/vm-import/latest/userguide/required-permissions.html): the name
# is fixed, VM Import/Export assumes it with the external id `vmimport`, reads the image from import/ and writes the snapshot
# and the AMI. tofu/ci's role may pass it (iam:PassRole on role/vmimport) and nothing else may use it.
resource "aws_iam_role" "vmimport" {
  name = "vmimport"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "vmie.amazonaws.com" }
      Condition = { StringEquals = { "sts:Externalid" = "vmimport" } }
    }]
  })
  tags = local.tags
}
resource "aws_iam_role_policy" "vmimport" {
  name = "vmimport"
  role = aws_iam_role.vmimport.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "ReadImages", Effect = "Allow", Action = ["s3:GetBucketLocation", "s3:ListBucket"], Resource = aws_s3_bucket.this.arn },
      { Sid = "ReadImageObjects", Effect = "Allow", Action = "s3:GetObject", Resource = "${aws_s3_bucket.this.arn}/import/*" },
      { Sid = "MakeSnapshotAndImage", Effect = "Allow", Action = ["ec2:ModifySnapshotAttribute", "ec2:CopySnapshot", "ec2:RegisterImage", "ec2:Describe*"], Resource = "*" },
    ]
  })
}

# ---- the Auto Scaling service-linked role. The module test's Auto Scaling group makes AWS create it on first use, and the CI role
# (rightly) may not iam:CreateServiceLinkedRole: in a fresh account the first group fails with AccessDenied. One per account: an
# account that has it already (any account that ever used Auto Scaling) imports it instead:
#   tofu import aws_iam_service_linked_role.autoscaling arn:aws:iam::<account>:role/aws-service-role/autoscaling.amazonaws.com/AWSServiceRoleForAutoScaling
resource "aws_iam_service_linked_role" "autoscaling" {
  aws_service_name = "autoscaling.amazonaws.com"
}

output "oidc_provider_arn" {
  description = "tofu/ci's oidc_provider_arn."
  value       = aws_iam_openid_connect_provider.github.arn
}
output "bucket" {
  description = "tofu/ci's bucket, and the NATKIT_BUCKET repository variable."
  value       = aws_s3_bucket.this.id
}
output "vmimport_role_arn" {
  description = "The role VM Import/Export assumes (tofu/ci's role passes it by name)."
  value       = aws_iam_role.vmimport.arn
}
output "ci_backend_config" {
  description = "The contents of tofu/ci/backend.tfvars (gitignored): `tofu output -raw ci_backend_config > ../ci/backend.tfvars`."
  value       = "bucket = \"${aws_s3_bucket.this.id}\"\nkey    = \"tofu/ci/terraform.tfstate\"\nregion = \"${data.aws_region.current.region}\"\n"
}
