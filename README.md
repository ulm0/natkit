# natkit

A NAT instance AMI for AWS, arm64 and x86_64: [fck-nat](https://github.com/AndrewGuenther/fck-nat)'s features on
[LinuxKit](https://github.com/linuxkit/linuxkit) instead of Amazon Linux, systemd and Packer. The OS is an immutable
in-memory image: a minimal kernel ([lk-kernel](https://github.com/ulm0/lk-kernel), shared with yagan and limakit), containerd, and a handful of service containers. There's no package
manager, SSH or SSM agent.

Docs: [natkit.ulm0.com](https://natkit.ulm0.com).

| fck-nat | natkit |
|---|---|
| Amazon Linux 2023, systemd unit `fck-nat.service` | LinuxKit `onboot` container `natkit-init` |
| `/etc/fck-nat.conf` written by a cloud-init script | YAML user-data with the same keys |
| `aws` CLI | `curl --aws-sigv4` with the instance role |
| `iptables -t nat ... MASQUERADE` | one nftables ruleset, applied atomically |
| Jool 4.1.15 built with DKMS, only in the separate `nat64-` AMIs | Jool 4.1.15 modules built against natkit's kernel, in every AMI |
| CloudWatch agent installed | CloudWatch agent pulled by digest at boot, only when enabled |
| SSH / SSM | EC2 Serial Console (root password hash from SSM, optional) |

## Usage

The AMIs aren't public. Import a [release](https://github.com/ulm0/natkit/releases)'s disk image into your own
account and region (or [build it](#building)):

```sh
V=v0.1.4 ARCH=arm64 # or amd64
curl -fsSLO "https://github.com/ulm0/natkit/releases/download/$V/natkit-$V-$ARCH-efi.img.gz"
gunzip "natkit-$V-$ARCH-efi.img.gz"
AWS_REGION=us-east-1 NATKIT_BUCKET=my-import-bucket scripts/ami.sh "natkit-$V-$ARCH-efi.img" $ARCH "natkit-$V-$ARCH"
```

`scripts/ami.sh` (in this repo) uploads the image to the bucket, imports it as a snapshot and registers the AMI. It
needs the [`vmimport` service role](https://docs.aws.amazon.com/vm-import/latest/userguide/required-permissions.html).

Launch the AMI (`natkit-<version>-arm64` or `-amd64`) in a public subnet with a public IP, turn off source/dest
check on its ENI, and point the private subnets' default route at it, as you would for fck-nat. With `eni_id`, natkit
turns off the primary ENI's check; create the static ENI with source/dest check off, since natkit doesn't change it.
IMDSv2 is required by the AMI.

User-data (every key optional; none at all is a plain NAT out of the primary ENI):

```yaml
eip_id: eipalloc-0123                 # associate this EIP with the primary ENI (after eni_id's ENI is attached)
eni_id: eni-0123                      # HA: attach this static ENI (the route tables' target) as device 1,
                                      #   turn off the primary ENI's source/dest check (create the static ENI with it off)
interface: eth0                       # public interface, if not the primary ENI's (ignored with eni_id)
client_cidrs: [10.0.0.0/16]           # only these sources may open connections through the NAT (default: any)
nat64: false                          # no NAT64 (default: on)
ip_local_port_range: "1024 61000"     # tuning, applied only when set (below Jool's ports, 61001-65535)
nf_conntrack_max: 262144              # default: one flow per 8 kB of RAM, and at least EC2's allowance
nf_conntrack_buckets: 65536           # default: a quarter of nf_conntrack_max
nf_conntrack_tcp_timeout_established: 86400
tcp_keepalive_time: 600
tcp_max_syn_backlog: 4096
metrics: nat-a                        # EC2's conntrack allowance counters to CloudWatch, without the agent
cwagent_enabled: true                 # CloudWatch agent with the JSON config in this SSM parameter
cwagent_cfg_param_name: /natkit/cwagent
console_password: /natkit/root-hash   # SSM parameter holding `openssl passwd -6` output: serial console login
lifecycle_hook: natkit-ready          # the group's launch lifecycle hook, completed with the ENI (before the EIP)
```

Unknown keys and malformed values stop the setup. The result is printed as one line on the serial console
(`aws ec2 get-console-output --latest`):

```
natkit: ready, public eth0 private eth1, eip eipalloc-0123, nat64 on, cwagent on
natkit: FAIL <reason>
```

For HA, run it in an Auto Scaling group of one with `eni_id`, `eip_id` and `lifecycle_hook`, and a launch lifecycle
hook on the group. A replacement sets itself up, takes the ENI over from the instance that holds it, tells the group
it's in service, and takes the EIP only once it has the ENI (fck-nat takes the EIP first, which moves egress off the
EIP while the old instance is still routing). Held by the hook, a replacement that fails its setup is never put in
service, and the old instance keeps routing. Without the hook, one that can't take the ENI over retries the
attachment for up to 15 minutes, as fck-nat does. See
[High availability](https://natkit.ulm0.com/docs/how-it-works/high-availability/).

NAT64 is on unless `nat64: false`: Jool translates `64:ff9b::/96` (pair it with Route 53 Resolver DNS64 on IPv6
subnets and a `64:ff9b::/96` route to the instance, whose ENI needs an IPv6 address). Jool's default IPv4 pool is
ports 61001-65535 of the instance's address: keep `ip_local_port_range` below that.

### OpenTofu

`tofu/nat` is that HA setup as a module: an ASG of one with the launch lifecycle hook, a static ENI, an EIP, the
routes and a scoped instance role. Unlike the AMI's defaults, NAT64 is off unless `nat64 = true` (which also adds
the `64:ff9b::/96` routes and the ENI's IPv6 address), and `client_cidrs` defaults to the VPC's CIDRs. Its inputs:
[OpenTofu module](https://natkit.ulm0.com/docs/deploy/opentofu/).

```hcl
module "nat" {
  source          = "github.com/ulm0/natkit//tofu/nat?ref=v0.1.4"
  vpc_id          = aws_vpc.main.id
  subnet_id       = aws_subnet.public.id
  route_table_ids = [aws_route_table.private.id]
  ami_id          = "ami-..." # natkit-v0.1.4-arm64: the same release as the module
  settings        = { nf_conntrack_max = "262144" }
}
```

### Instance role

Only needed for the keys that call AWS:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Action": "ec2:AssociateAddress", "Resource": "*"},
    {"Effect": "Allow", "Action": ["ec2:AttachNetworkInterface", "ec2:ModifyNetworkInterfaceAttribute"], "Resource": "*"},
    {"Effect": "Allow", "Action": ["ec2:DescribeNetworkInterfaces", "ec2:DetachNetworkInterface"], "Resource": "*"},
    {"Effect": "Allow", "Action": ["autoscaling:DescribeAutoScalingInstances", "autoscaling:RecordLifecycleActionHeartbeat", "autoscaling:CompleteLifecycleAction"], "Resource": "*"},
    {"Effect": "Allow", "Action": "ssm:GetParameter", "Resource": "arn:aws:ssm:*:*:parameter/natkit/*"}
  ]
}
```

plus `cloudwatch:PutMetricData` with `metrics`, and `CloudWatchAgentServerPolicy` with `cwagent_enabled`. Scope `Resource` down to your EIP, ENI and instances
where you can.

The CloudWatch agent image (~150 MB unpacked) is pulled from `public.ecr.aws` through the NAT's own egress and kept
in memory.

Instance size: 0.5 GiB of memory is enough (`t4g.nano` on arm64, `t3.nano` or `t3a.nano` on amd64). At boot the
read-only root (a 66 MB squashfs) is copied from the EBS volume into memory, which is all natkit reads from the disk.
The CloudWatch agent needs 1 GiB (`t4g.micro`, `t3.micro`): its image doesn't fit in a nano's memory.

What a deployment is billed for (the instance, its 1 GiB volume, the public IPv4 address, CPU credits, data transfer,
the optional metrics) is in the docs: [Costs](https://natkit.ulm0.com/docs/operate/costs/).

## Hardening

- No SSH, SSM agent or package manager; the only login is the EC2 Serial Console, locked unless `console_password`
  is set.
- The instance accepts no new inbound connections to itself (nftables input policy drop: replies, ICMP and DHCP
  only). It forwards new connections only from `client_cidrs`, when set (the module always sets it), and drops
  packets conntrack calls invalid, which would otherwise leave un-NATed.
- AWS credentials reach `curl` on stdin, never on a command line.
- Kernel: built up from `allnoconfig`, without USB, wireless, `/dev/mem`, kexec or 32-bit syscalls, and with CPU
  mitigations, KASLR, stack protector, hardened usercopy and zeroed allocations. It is shared with yagan and limakit,
  so it also carries what they need (KVM, kprobes and perf, the BPF JIT, WireGuard, IPVS, and filesystems such as
  ext4, xfs, btrfs and NFS); natkit itself uses squashfs, tmpfs and overlay. Module loading is disabled once Jool is in.
- No ICMP redirects or source routing; unprivileged BPF and kernel log access are off.
- `natkit-init` runs with three capabilities; the CloudWatch agent launcher with `CAP_SYS_ADMIN` only (`ctr run`
  mounts the agent image's rootfs; its containerd socket is root-equivalent anyway).
- Third-party images are pinned by digest. natkit's own init and Jool images are built from the same commit in CI (a
  local `make` without `make jool` pulls the Jool modules of main's latest build). The Jool source is pinned by
  checksum. IMDSv2 only.

## Building

Everything builds natively for the Docker daemon's architecture (arm64 or amd64):

```sh
make jool          # Jool modules for natkit.yml's kernel (lk-kernel); or let linuxkit pull them from GHCR
make               # out/natkit-<arch>-efi.img
make test          # natkit-init against a mock IMDS/EC2/SSM
make ami           # needs AWS credentials, AWS_REGION, NATKIT_BUCKET and the vmimport role
terraform-docs tofu/nat   # the OpenTofu module page's Inputs and Outputs, from tofu/nat's descriptions
terraform-docs examples/vpc   # the VPC module page's example, from examples/vpc/main.tf
hugo --source docs && python3 docs/check-links.py docs/public   # the docs site, and its links
```

## CI

`.github/workflows/build.yml` builds both architectures for pushes to `main`, `v*` tags and pull requests, unless
they change only the docs. Tags, and manual runs on `main` that keep `boot_test` on, also register
the AMIs and run `scripts/boot-test.sh`: an EIP, a static ENI, a client in a private subnet whose egress must be the
EIP, a handover to a second instance, CloudWatch agent metrics, NAT64 when the test subnets have IPv6, and a 0.5 GiB
instance with its root in memory and the allowance metrics. Then
`scripts/module-test.sh` applies `tofu/nat` (arm64): the handover's time without egress, a replacement that fails,
the conntrack alarm's metric, `client_cidrs`, `untracked`, the idle timeout, and NAT64 at every stage.
`tofu/ci` creates the role, test VPC and instance profile and outputs these repository variables (set `AWS_ROLE_ARN` as a secret
instead: it holds the account ID, and the run logs are public):

| Variable | |
|---|---|
| `AWS_ROLE_ARN` (secret) | GitHub OIDC role, assumable only from `main` and `v*` tags. It imports and registers AMIs and deletes the ones it tagged `natkit.io/release` (per-commit AMIs are pruned after each run, and a release's AMIs once a higher release is out). For the tests it creates instances, EIPs, ENIs, security groups and a launch template with a test's tag, and an Auto Scaling group and an alarm named `natkit-module-test-*`; it changes or deletes only those, what carries a test's tag or is in the test VPC. Beyond those, it writes under `import/` and `module-test/` in the bucket and the `/natkit/boot-test/*` parameters, and can tag any imported or copied snapshot in the account for release. `iam:PassRole` for the test profile and the `vmimport` role only |
| `AWS_REGION` | the region for everything |
| `NATKIT_BUCKET` | S3 bucket for image imports |
| `NATKIT_TEST_PUBLIC_SUBNET` | subnet with an internet gateway route and an IPv6 CIDR (the module test always checks NAT64) |
| `NATKIT_TEST_PRIVATE_SUBNET` | same AZ, with an IPv6 CIDR, its own route table and no other egress (no NAT or egress-only gateway): the tests own its default route |
| `NATKIT_TEST_INSTANCE_PROFILE` | the instance role above, limited to the test VPC's interfaces, instances with this profile and EIPs a test tagged, + `cloudwatch:PutMetricData` in the `natkit` and `natkit-boot-test` namespaces. `natkit` is also production NATs' namespace: keep those in another account |

## License

MIT. natkit-init follows fck-nat's `service/fck-nat.sh` (MIT, Andrew Guenther).
