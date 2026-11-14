---
title: CloudWatch
description: The allowance counters without an agent, or the CloudWatch agent, pulled at boot.
weight: 70
icon: monitoring
---

## Allowance metrics

natkit can publish EC2's [connection-tracking allowance](../performance/) counters itself, without the agent:

```yaml
metrics: nat-a
```

About once a minute it sends two metrics to the `natkit` namespace, with the dimension `Name=nat-a`:

| Metric | |
|---|---|
| `conntrack_allowance_exceeded` | packets EC2 dropped because the allowance ran out, since the last report, on any interface. Alarm on its sum being above 0 |
| `conntrack_allowance_available` | tracked connections left before it runs out |

- It is a shell loop and one signed request: it runs on a 0.5 GiB instance.
- The instance role needs `cloudwatch:PutMetricData`. It can be limited to the namespace with the condition `cloudwatch:namespace = natkit`.
- Use a name that outlives the instance, such as the Auto Scaling group's: the instance ID changes with every replacement.
- The console says `natkit-metrics: publishing to CloudWatch, namespace natkit, Name=nat-a` when it starts, and `natkit-metrics: can't publish: <reason>` if it stops.
- A report is a new connection too. While the allowance is exhausted, EC2 drops it like any other: the metrics have no data until a report gets through, and that one covers the whole time. An alarm should react to missing data as well.
- CloudWatch bills it: two custom metrics, about 52 000 requests a month and, with the module, one alarm. At list prices in US East (N. Virginia) that is about $1.20 a month for each NAT, before the free tier. See [Costs](../costs/#cloudwatch).

The [OpenTofu module](../opentofu/) sets this with `conntrack_alarm_actions`, along with the alarm.

## The CloudWatch agent

For anything else (memory, CPU, network counters), run the CloudWatch agent with these user-data keys:

```yaml
cwagent_enabled: true
cwagent_cfg_param_name: /natkit/cwagent   # SSM parameter with the agent's JSON config
```

The instance role also needs `CloudWatchAgentServerPolicy` and `ssm:GetParameter` on the parameter.

The agent image is not in the AMI. natkit pulls it by digest from `public.ecr.aws` at boot, only when the agent is enabled, through the NAT's own egress. It is about 150 MB unpacked and stays in memory: use at least a `t4g.micro` or `t3.micro`. On a 0.5 GiB instance the image doesn't fit, and the console repeats `natkit-cwagent: pull failed, retrying: ... no space left on device`.
