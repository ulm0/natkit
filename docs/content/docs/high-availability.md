---
title: High availability
description: An Auto Scaling group of one, a static ENI and an EIP.
weight: 30
icon: sync_alt
---

For HA, run natkit in an Auto Scaling group of one, with `eni_id` and `eip_id` in its user-data:

- **Static ENI (`eni_id`):** the private route tables point at this ENI, not at an instance. The instance attaches it as device 1.
- **EIP (`eip_id`):** the instance associates it with its primary ENI, so egress keeps the same public IP across replacements.

When an instance is replaced:

1. The new instance keeps retrying the ENI attachment while the old one still holds it, for up to 15 minutes.
2. Once it has the ENI, it takes the EIP.

fck-nat takes the EIP first. That moves egress off the EIP while the old instance is still routing. natkit waits for the ENI first to avoid that gap.

The [OpenTofu module](../opentofu/) sets up this layout.
