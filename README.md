# natkit

A NAT instance AMI for AWS, arm64 and x86_64: [fck-nat](https://github.com/AndrewGuenther/fck-nat)'s features on
[LinuxKit](https://github.com/linuxkit/linuxkit) instead of Amazon Linux, systemd and Packer. The OS is an immutable
in-memory image: a minimal kernel ([lk-kernel](https://github.com/ulm0/lk-kernel), shared with yagan and limakit), containerd, and a handful of service containers. There's no package
manager, SSH or SSM agent.

| fck-nat | natkit |
|---|---|
| Amazon Linux 2023, systemd unit `fck-nat.service` | LinuxKit `onboot` container `natkit-init` |
| `/etc/fck-nat.conf` written by a cloud-init script | YAML user-data with the same keys |
| `aws` CLI | `curl --aws-sigv4` with the instance role |
| `iptables -t nat ... MASQUERADE` | one nftables ruleset, applied atomically |
| Jool from packages | Jool 4.1.15 modules built against natkit's kernel |
| CloudWatch agent installed | CloudWatch agent pulled by digest at boot, only when enabled |
| SSH / SSM | EC2 Serial Console (root password hash from SSM, optional) |

## Usage

Launch the AMI (`natkit-<version>-arm64` or `-amd64`) in a public subnet with a public IP, turn off source/dest
check on its ENI (or use `eni_id`), and point the private subnets' default route at it, as you would for fck-nat.
IMDSv2 is required by the AMI.

User-data (every key optional; none at all is a plain NAT out of the primary ENI):

```yaml
eip_id: eipalloc-0123                 # associate this EIP with the primary ENI (after eni_id's ENI is attached)
eni_id: eni-0123                      # HA: attach this static ENI (the route tables' target) as device 1,
                                      #   turn off the primary ENI's source/dest check
interface: eth0                       # public interface, if not the primary ENI's (ignored with eni_id)
nat64: false                          # no NAT64 (default: on)
ip_local_port_range: "1024 65535"     # tuning, applied only when set
nf_conntrack_max: 262144
nf_conntrack_buckets: 65536
nf_conntrack_tcp_timeout_established: 86400
tcp_keepalive_time: 600
tcp_max_syn_backlog: 4096
cwagent_enabled: true                 # CloudWatch agent with the JSON config in this SSM parameter
cwagent_cfg_param_name: /natkit/cwagent
console_password: /natkit/root-hash   # SSM parameter holding `openssl passwd -6` output: serial console login
```

Unknown keys and malformed values stop the setup. The result is printed as one line on the serial console
(`aws ec2 get-console-output --latest`):

```
natkit: ready, public eth0 private eth1, eip eipalloc-0123, nat64 on, cwagent on
natkit: FAIL <reason>
```

For HA, run it in an Auto Scaling group of one with `eni_id` and `eip_id`. A replacement keeps retrying the ENI
attachment for up to 15 minutes while the old instance still holds it, and takes the EIP only once it has the ENI
(fck-nat takes the EIP first, which moves egress off the EIP while the old instance is still routing).

NAT64 is on unless `nat64: false`: Jool translates `64:ff9b::/96` (pair it with Route 53 Resolver DNS64 on IPv6
subnets and a `64:ff9b::/96` route to the instance, whose ENI needs an IPv6 address). Jool's default IPv4 pool is
ports 61001-65535 of the instance's address: keep `ip_local_port_range` below that.

### OpenTofu

`tofu/nat` is that HA setup as a module (ASG of one, static ENI, EIP, routes, a scoped instance role):

```hcl
module "nat" {
  source          = "github.com/ulm0/natkit//tofu/nat"
  vpc_id          = aws_vpc.main.id
  subnet_id       = aws_subnet.public.id
  route_table_ids = [aws_route_table.private.id]
  ami_id          = "ami-..." # natkit-<version>-arm64
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
    {"Effect": "Allow", "Action": "ssm:GetParameter", "Resource": "arn:aws:ssm:*:*:parameter/natkit/*"}
  ]
}
```

plus `CloudWatchAgentServerPolicy` with `cwagent_enabled`. Scope `Resource` down to your EIP, ENI and instances
where you can.

The CloudWatch agent image (~150 MB unpacked) is pulled from `public.ecr.aws` through the NAT's own egress and kept
in memory: use at least a `t4g.micro`/`t3.micro` with it.

## Hardening

- No SSH, SSM agent or package manager; the only login is the EC2 Serial Console, locked unless `console_password`
  is set.
- The instance accepts no new inbound connections to itself (nftables input policy drop: replies, ICMP and DHCP
  only). Forwarded traffic is not filtered (use the security group), except packets conntrack calls invalid, which
  would otherwise leave un-NATed.
- AWS credentials reach `curl` on stdin, never on a command line.
- Kernel: built from `allnoconfig` (no USB, wireless, filesystems beyond tmpfs/overlay, `/dev/mem`, kexec or 32-bit
  syscalls), CPU mitigations, KASLR, stack protector, hardened usercopy, zeroed allocations. Module loading is
  disabled once Jool is in.
- No ICMP redirects or source routing; unprivileged BPF and kernel log access are off.
- `natkit-init` runs with three capabilities; the CloudWatch agent launcher with `CAP_SYS_ADMIN` only (`ctr run`
  mounts the agent image's rootfs; its containerd socket is root-equivalent anyway).
- Images are pinned by digest or content hash; the Jool source by checksum. IMDSv2 only.

## Building

Everything builds natively for the Docker daemon's architecture (arm64 or amd64):

```sh
make jool          # Jool modules for natkit.yml's kernel (lk-kernel); or let linuxkit pull them from GHCR
make               # out/natkit-<arch>-efi.img
make test          # natkit-init against a mock IMDS/EC2/SSM
make ami           # needs AWS credentials, AWS_REGION, NATKIT_BUCKET and the vmimport role
```

## CI

`.github/workflows/build.yml` builds both architectures on every push. Tags (`v*`) and manual runs also register
the AMIs and run `scripts/boot-test.sh`: an EIP, a static ENI, a client in a private subnet whose egress must be the
EIP, a failover to a second instance, CloudWatch agent metrics, and NAT64 when the test subnets have IPv6.
`tofu/ci` creates the role, test VPC and instance profile and outputs these repository variables (set `AWS_ROLE_ARN` as a secret
instead: it holds the account ID, and the run logs are public):

| Variable | |
|---|---|
| `AWS_ROLE_ARN` (secret) | GitHub OIDC role: S3 + `ec2:ImportSnapshot`/`RegisterImage` (as yagan), and for the boot test EC2 instances, EIPs, ENIs, routes, `ssm:PutParameter`/`DeleteParameter` under `/natkit/boot-test/`, `iam:PassRole` for the test profile, `cloudwatch:GetMetricStatistics` |
| `AWS_REGION` | the region for everything |
| `NATKIT_BUCKET` | S3 bucket for image imports |
| `NATKIT_TEST_PUBLIC_SUBNET` | subnet with an internet gateway route |
| `NATKIT_TEST_PRIVATE_SUBNET` | same AZ, its own route table, no other egress: the boot test owns its default route |
| `NATKIT_TEST_INSTANCE_PROFILE` | the instance role above + `CloudWatchAgentServerPolicy` |

## License

MIT. natkit-init follows fck-nat's `service/fck-nat.sh` (MIT, Andrew Guenther).
