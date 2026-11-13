---
title: Hardening
description: What natkit leaves out and locks down.
weight: 80
icon: shield
---

- **Login:** no SSH, SSM agent or package manager. The only login is the EC2 Serial Console. It stays locked unless `console_password` is set.
- **Inbound:** the instance accepts no new inbound connections to itself. The nftables input policy is drop: replies, ICMP and DHCP only.
- **Forwarded traffic:** not filtered (use the security group), except packets conntrack calls invalid, which would otherwise leave un-NATed.
- **Credentials:** AWS credentials reach `curl` on stdin, never on a command line.
- **Kernel:** built from `allnoconfig`, with no USB, wireless, filesystems beyond tmpfs/overlay, `/dev/mem`, kexec or 32-bit syscalls. CPU mitigations, KASLR, stack protector, hardened usercopy and zeroed allocations are on. Module loading is disabled once Jool is in.
- **Network stack:** no ICMP redirects or source routing. Unprivileged BPF and kernel log access are off.
- **Capabilities:** `natkit-init` runs with three capabilities. The CloudWatch agent launcher runs with `CAP_SYS_ADMIN` only: `ctr run` mounts the agent image's rootfs, and its containerd socket is root-equivalent anyway.
- **Supply chain:** images are pinned by digest or content hash, and the Jool source by checksum. IMDSv2 only.
