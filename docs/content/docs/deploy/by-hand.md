---
title: By hand
description: One instance from the console or the CLI, or an Auto Scaling group of your own.
weight: 30
---

Without the [OpenTofu module](/docs/deploy/opentofu/), launch the AMI yourself:

1. Launch the AMI in a public subnet with a public IP. Pick `natkit-<version>-arm64` or `natkit-<version>-amd64` to match the instance type.
2. Turn off source/dest check on its ENI, or let natkit do it with [`eni_id`](/docs/reference/configuration/).
3. Point the private subnets' default route at it, as you would for fck-nat.

The AMI requires IMDSv2.

Without user-data, natkit is a plain NAT out of the primary ENI. The [user-data keys](/docs/reference/configuration/) add the rest, and the ones that call AWS need an [instance role](/docs/reference/instance-role/).

For high availability, run it in an Auto Scaling group of one with a static ENI and an EIP. [High availability](/docs/how-it-works/high-availability/) has the layout, and the lifecycle hook to add to a group of your own.

What it costs: see [Costs](/docs/operate/costs/#launched-by-hand).
