---
title: Building
description: Build the image and AMI, and how CI tests them.
weight: 100
icon: build
---

Everything builds natively for the Docker daemon's architecture (arm64 or amd64):

```sh
make jool          # Jool modules for natkit.yml's kernel (lk-kernel); or let linuxkit pull them from GHCR
make               # out/natkit-<arch>-efi.img
make test          # natkit-init against a mock IMDS/EC2/SSM
make ami           # needs AWS credentials, AWS_REGION, NATKIT_BUCKET and the vmimport role
```

## CI

`.github/workflows/build.yml` builds both architectures on every push. Tags (`v*`) and manual runs also register the AMIs and run `scripts/boot-test.sh`, which checks:

- an EIP and a static ENI;
- a client in a private subnet whose egress must be the EIP;
- a handover to a second instance, which takes the ENI over;
- CloudWatch agent metrics;
- NAT64, when the test subnets have IPv6.

On arm64, `scripts/module-test.sh` then applies the [OpenTofu module](../opentofu/) and checks:

- an instance refresh leaves the client without egress for at most 30 seconds;
- the conntrack alarm's metric is published;
- a client outside `client_cidrs` gets nothing through, with `untracked`;
- `tcp_established_timeout` reaches both ENIs.

`tofu/ci` creates the CI role, test VPC and instance profile. See the [README](https://github.com/ulm0/natkit#ci) for the repository variables.
