---
title: Monitoring
description: EC2's allowance counters and their alarm without an agent, or the CloudWatch agent.
weight: 10
aliases: [/docs/cloudwatch/]
---

Two ways to watch natkit in CloudWatch: the allowance metrics natkit publishes itself, with their alarm, and the CloudWatch agent for everything else.

## Allowance metrics

natkit can publish EC2's [connection-tracking](/docs/operate/performance/), bandwidth and packets-per-second allowance counters itself, without the agent:

```yaml
metrics: nat-a
```

About once a minute it sends five metrics to the `natkit` namespace, with the dimension `Name=nat-a`:

| Metric | |
|---|---|
| `conntrack_allowance_exceeded` | packets EC2 dropped because the allowance ran out, since the last report, on any interface. Alarm on its sum being above 0 |
| `conntrack_allowance_available` | tracked connections left before it runs out |
| `bw_in_allowance_exceeded` | packets EC2 queued or dropped because inbound traffic went over the instance's bandwidth allowance, since the last report, on any interface |
| `bw_out_allowance_exceeded` | the same for outbound traffic |
| `pps_allowance_exceeded` | the same for packets per second, both directions |

- The bandwidth and packets-per-second allowances are the instance type's network limits. A burstable type's [burst rate](/docs/operate/performance/) falls back to its baseline once its network credits run out, and these counters are where that shows. A sum above 0 means the instance type is too small for the traffic, not that natkit is.
- It is one signed request a minute from natkit's own binary, no agent: it runs on a 0.5 GiB instance.
- The instance role needs `cloudwatch:PutMetricData`. It can be limited to the namespace with the condition `cloudwatch:namespace = natkit`.
- Use a name that outlives the instance, such as the Auto Scaling group's: the instance ID changes with every replacement.
- The console says `natkit-metrics: publishing to CloudWatch, namespace natkit, Name=nat-a` when it starts, and `natkit-metrics: can't publish: <reason>` if it stops.
- A report is a new connection too. While the allowance is exhausted, EC2 drops it like any other: the metrics have no data until a report gets through, and that one covers the whole time. An alarm should react to missing data as well.
- CloudWatch bills it: five custom metrics, about 52 000 requests a month and, with the module, one alarm. At list prices in US East (N. Virginia) that is about $2.10 a month for each NAT, before the free tier. See [Costs](/docs/operate/costs/#cloudwatch).

## Alarms

With the [OpenTofu module](/docs/deploy/opentofu/), `conntrack_alarm_actions` sets this up: natkit publishes the counters and a CloudWatch alarm goes off when EC2 drops packets, or when the NAT stops reporting.

```hcl
conntrack_alarm_actions = [aws_sns_topic.alerts.arn]
```

Without the module, set [`metrics`](#allowance-metrics) in the user-data and alarm on the sum of `conntrack_allowance_exceeded` being above 0, and on missing data. It should stay at 0. To act before EC2 drops anything, alarm on `conntrack_allowance_available` getting low too.

The module's alarm watches `conntrack_allowance_exceeded` only. To be told when the instance type's network limits are reached too, add an alarm of your own on the bandwidth and packets-per-second counters, for example:

```hcl
resource "aws_cloudwatch_metric_alarm" "network_allowance" {
  alarm_name          = "nat-a-network-allowance-exceeded"
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  alarm_actions       = [aws_sns_topic.alerts.arn]
  # the counters include packets EC2 only queued: alarm on a limit that lasts
  evaluation_periods  = 5
  datapoints_to_alarm = 3
  # the module's alarm reports missing data
  treat_missing_data = "notBreaching"

  metric_query {
    id          = "exceeded"
    expression  = "bw_in + bw_out + pps"
    return_data = true
  }
  dynamic "metric_query" {
    for_each = { bw_in = "bw_in_allowance_exceeded", bw_out = "bw_out_allowance_exceeded", pps = "pps_allowance_exceeded" }
    content {
      id = metric_query.key
      metric {
        namespace   = "natkit"
        metric_name = metric_query.value
        dimensions  = { Name = "nat-a" }
        stat        = "Sum"
        period      = 60
      }
    }
  }
}
```

## The CloudWatch agent

For anything else (memory, CPU, network counters), run the CloudWatch agent with these user-data keys:

```yaml
cwagent_enabled: true
cwagent_cfg_param_name: /natkit/cwagent   # SSM parameter with the agent's JSON config
```

The instance role also needs `CloudWatchAgentServerPolicy` and `ssm:GetParameter` on the parameter.

The agent image is not in the AMI. natkit pulls it by digest from `public.ecr.aws` at boot, only when the agent is enabled, through the NAT's own egress. It is about 150 MB unpacked and stays in memory: use at least a `t4g.micro` or `t3.micro`. On a 0.5 GiB instance the image doesn't fit, and the console repeats `natkit-cwagent: pull failed, retrying: ... no space left on device`.

natkit publishes the bandwidth and packets-per-second allowance counters itself, with [`metrics`](#allowance-metrics). The agent can publish them too, per interface and with the instance's own dimensions. Add this to its config:

```json
{
  "metrics": {
    "append_dimensions": { "InstanceId": "${aws:InstanceId}" },
    "metrics_collected": {
      "ethtool": {
        "interface_include": ["eth0", "eth1"],
        "metrics_include": [
          "pps_allowance_exceeded",
          "bw_in_allowance_exceeded",
          "bw_out_allowance_exceeded"
        ]
      }
    }
  }
}
```

- The agent's `ethtool_pps_allowance_exceeded`, `ethtool_bw_in_allowance_exceeded` and `ethtool_bw_out_allowance_exceeded` are the driver's running counts of dropped packets. They should stay flat.
