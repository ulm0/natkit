---
title: Building
description: Build the image and AMI, and how CI tests them.
weight: 10
aliases: [/docs/building/]
---

Everything builds natively for the Docker daemon's architecture (arm64 or amd64):

```sh
make jool          # Jool modules for natkit.yml's kernel (lk-kernel); or let linuxkit pull them from GHCR
make               # out/natkit-<arch>-efi.img
make test          # natkit's Go unit tests, then the image against a mock IMDS, EC2, SSM, Auto Scaling and CloudWatch
cd init && go test ./...   # the unit tests alone (Go 1.27)
make ami           # needs AWS credentials, AWS_REGION, NATKIT_BUCKET and the vmimport role
terraform-docs tofu/nat   # the OpenTofu module page's Inputs and Outputs, from tofu/nat's descriptions
terraform-docs examples/vpc   # the VPC module page's example, from examples/vpc/main.tf
hugo --source docs && python3 docs/check-links.py docs/public   # the docs site, and its links
```

## CI

natkit's own code is one Go binary in `init/`, on the AWS SDK for Go v2: `natkit-init` (onboot), `natkit-metrics` and `natkit-cwagent` (services). Its unit tests run against fakes of IMDS, AWS and the host, and the SDK's clients against a fake of each API on the wire.

`.github/workflows/build.yml` builds both architectures for pushes to `main`, `v*` tags and pull requests, unless they change only the docs (`.github/workflows/docs.yml` builds and checks those). Tags, and manual runs on `main` that keep `boot_test` on, also register the AMIs and run `scripts/boot-test.sh`, which checks:

- an EIP and a static ENI;
- a client in a private subnet whose egress must be the EIP;
- a handover to a second instance, which takes the ENI over;
- CloudWatch agent metrics;
- NAT64, when the test subnets have IPv6;
- a 0.5 GiB instance: its root in memory, and the allowance metrics published.

On arm64, `scripts/module-test.sh` then applies the [OpenTofu module](/docs/deploy/opentofu/) and checks:

- an instance refresh leaves the client without egress for at most 30 seconds;
- a replacement whose setup fails doesn't stop egress;
- the conntrack alarm's metric is published;
- a client outside `client_cidrs` gets nothing through, with `untracked`;
- `tcp_established_timeout` reaches both ENIs;
- NAT64 at every stage: both test subnets need an IPv6 CIDR, and the private one no egress-only gateway.

Every push also checks the Go code: `gofmt`, `go vet`, the unit tests with the race detector, `govulncheck` (known vulnerabilities in the code natkit calls), and the user-data parser fuzzed for a minute.

Every AMI is tagged `ec2:SharedTag/natkit.io/version`, `ec2:SharedTag/natkit.io/arch` and `ec2:SharedTag/natkit.io/commit`, which accounts it's shared with can read and filter on. Per-commit AMIs are deregistered after each run and are never public. A release, once both boot tests pass, makes its own AMIs public (in the CI Region only). A published release's AMIs are never deregistered, and the CI role can't deregister a public AMI; a lower release's that never went public (its tests failed) are. The account blocks public AMIs; the release unblocks that only for its two calls and blocks it again, and the CI role can make public only an AMI tagged as a release's. Public sharing of snapshots stays blocked: a public AMI doesn't need it.

## A pull request's EC2 tests

CI's role only takes `main` and release tags, so a branch can't run the boot and module tests there. `scripts/pr-test.sh` runs them from a laptop, against the disk images the pull request's own `build` run made:

```sh
git switch <the pull request's branch>
export AWS_PROFILE=... AWS_REGION=... NATKIT_BUCKET=... \
  NATKIT_TEST_PUBLIC_SUBNET=... NATKIT_TEST_PRIVATE_SUBNET=... NATKIT_TEST_INSTANCE_PROFILE=...
scripts/pr-test.sh <pull request number>   # TF=terraform without tofu
```

It registers an AMI per architecture, boot-tests each, module-tests the arm64 one, and removes the AMIs and their snapshots when it ends. It takes about an hour, and waits while another test routes the private subnet.

`tofu/ci` creates the CI role, test VPC and instance profile. Both roles change only the test VPC, what a test tagged and the module test's group and alarm, plus the import bucket's `import/` and `module-test/`, the `/natkit/boot-test/*` parameters and imported snapshots. The test role publishes to the `natkit` namespace, which production NATs use too: keep those in another account. See the [README](https://github.com/ulm0/natkit#ci) for the repository variables.
