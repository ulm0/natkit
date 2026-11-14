---
title: Configuration
description: The YAML user-data keys.
weight: 20
icon: tune
---

natkit reads its settings from YAML user-data, with the same keys as fck-nat's `/etc/fck-nat.conf`. Every key is optional: with none at all, natkit is a plain NAT out of the primary ENI.

```yaml
eip_id: eipalloc-0123                 # associate this EIP with the primary ENI (after eni_id's ENI is attached)
eni_id: eni-0123                      # HA: attach this static ENI (the route tables' target) as device 1,
                                      #   turn off the primary ENI's source/dest check
interface: eth0                       # public interface, if not the primary ENI's (ignored with eni_id)
client_cidrs: [10.0.0.0/16]           # only these sources may open connections through the NAT (default: any)
nat64: false                          # no NAT64 (default: on)
ip_local_port_range: "1024 65535"     # tuning, applied only when set
nf_conntrack_max: 262144              # default: one flow per 8 kB of RAM (about 117 000 on 1 GiB)
nf_conntrack_buckets: 65536           # default: a quarter of nf_conntrack_max
nf_conntrack_tcp_timeout_established: 86400
tcp_keepalive_time: 600
tcp_max_syn_backlog: 4096
cwagent_enabled: true                 # CloudWatch agent with the JSON config in this SSM parameter
cwagent_cfg_param_name: /natkit/cwagent
console_password: /natkit/root-hash   # SSM parameter holding `openssl passwd -6` output: serial console login
```

`client_cidrs` is a list of IPv4 and IPv6 CIDRs. With it, natkit forwards a new connection only from those sources; replies to a forwarded connection always pass. NAT64 serves only its IPv6 CIDRs, so a list with none turns NAT64 away for everyone, and Jool lets in only the IPv4 hosts a client has contacted (address-dependent filtering). Without it, the security group alone decides who can use the NAT. The [OpenTofu module](../opentofu/) always sets it.

An unknown key or a malformed value stops the setup. The console then prints `natkit: FAIL <reason>`.

Keys that call AWS need an [instance role](../instance-role/).
