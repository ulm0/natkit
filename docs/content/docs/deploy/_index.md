---
title: Deploy
description: With the OpenTofu module, next to the VPC module, or by hand; and upgrades.
weight: 20
---

{{< cards >}}
  {{< card link="opentofu/" title="OpenTofu module" subtitle="tofu/nat sets up the HA layout." >}}
  {{< card link="vpc-module/" title="With the VPC module" subtitle="natkit's module next to terraform-aws-modules/vpc." >}}
  {{< card link="by-hand/" title="By hand" subtitle="One instance from the console or the CLI, or an Auto Scaling group of your own." >}}
  {{< card link="migrating/" title="From a NAT gateway" subtitle="Replace NAT gateways one Availability Zone at a time, keeping their Elastic IPs, and how to go back." >}}
  {{< card link="upgrading/" title="Upgrading" subtitle="How a new release reaches a running NAT, and what each one changes." >}}
{{< /cards >}}
