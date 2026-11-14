---
title: Instance role
description: The IAM permissions natkit needs.
weight: 60
icon: badge
---

natkit only needs an instance role for the keys that call AWS:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Action": "ec2:AssociateAddress", "Resource": "*"},
    {"Effect": "Allow", "Action": ["ec2:AttachNetworkInterface", "ec2:ModifyNetworkInterfaceAttribute"], "Resource": "*"},
    {"Effect": "Allow", "Action": ["ec2:DescribeNetworkInterfaces", "ec2:DetachNetworkInterface"], "Resource": "*"},
    {"Effect": "Allow", "Action": "ssm:GetParameter", "Resource": "arn:aws:ssm:*:*:parameter/natkit/*"}
  ]
}
```

The second EC2 statement lets a replacement instance [take the static ENI over](../high-availability/) from the one it replaces; without it, it waits for the ENI.

With `cwagent_enabled`, also attach `CloudWatchAgentServerPolicy`.

Scope `Resource` down to your EIP, ENI and instances where you can. The [OpenTofu module](../opentofu/)'s role does this: tag conditions on the ENIs and instances, and the one EIP and SSM parameters by ARN.
