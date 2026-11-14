---
title: CloudWatch agent
description: Metrics from the CloudWatch agent, pulled at boot.
weight: 70
icon: monitoring
---

To run the CloudWatch agent, set these user-data keys:

```yaml
cwagent_enabled: true
cwagent_cfg_param_name: /natkit/cwagent   # SSM parameter with the agent's JSON config
```

The instance role also needs `CloudWatchAgentServerPolicy` and `ssm:GetParameter` on the parameter.

The agent image is not in the AMI. natkit pulls it by digest from `public.ecr.aws` at boot, only when the agent is enabled, through the NAT's own egress. It is about 150 MB unpacked and stays in memory: use at least a `t4g.micro` or `t3.micro`. On a 0.5 GiB instance the image doesn't fit, and the console repeats `natkit-cwagent: pull failed, retrying: ... no space left on device`.
