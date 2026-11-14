---
title: natkit
layout: hextra-home
---

<div class="hx:mt-6 hx:mb-6">
{{< hextra/hero-headline >}}
  A NAT instance AMI for AWS
{{< /hextra/hero-headline >}}
</div>

<div class="hx:mb-12">
{{< hextra/hero-subtitle >}}
  [fck-nat](https://github.com/AndrewGuenther/fck-nat)'s features on LinuxKit, as an immutable in-memory image.&nbsp;<br class="hx:sm:block hx:hidden" />arm64 and x86_64. Open source, MIT licensed.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-6">
{{< hextra/hero-button text="Get started" link="docs/start/getting-started/" >}}
</div>

<div class="hx:mt-6"></div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card title="Immutable, in memory" icon="lock-closed"
    subtitle="A minimal kernel, containerd and a handful of service containers, all in memory: a `t4g.nano` is enough. No package manager, SSH or SSM agent." >}}
  {{< hextra/feature-card title="Highly available" icon="refresh"
    subtitle="An Auto Scaling group of one with a static ENI and an EIP. A replacement goes into service only once it has set itself up." >}}
  {{< hextra/feature-card title="NAT64" icon="switch-horizontal"
    subtitle="Jool translates `64:ff9b::/96` for IPv6-only subnets, paired with Route 53 Resolver DNS64." >}}
  {{< hextra/feature-card title="One YAML file" icon="document-text"
    subtitle="fck-nat's settings as YAML user-data. One line on the serial console says ready, or why not." >}}
  {{< hextra/feature-card title="Allowance metrics" icon="chart-bar"
    subtitle="EC2's connection-tracking allowance to CloudWatch without an agent, and an alarm when it runs out." >}}
  {{< hextra/feature-card title="OpenTofu module" icon="cube"
    subtitle="`tofu/nat` sets up the instance, ENI, EIP, routes, a scoped instance role and the alarm." >}}
{{< /hextra/feature-grid >}}
