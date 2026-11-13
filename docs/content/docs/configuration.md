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

An unknown key or a malformed value stops the setup. The console then prints `natkit: FAIL <reason>`.

Keys that call AWS need an [instance role](../instance-role/).
