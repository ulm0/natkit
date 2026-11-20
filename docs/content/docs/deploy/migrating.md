---
title: From a NAT gateway
description: Replace NAT gateways with natkit one Availability Zone at a time, keeping their Elastic IPs, and how to go back.
weight: 35
---

natkit can take over a NAT gateway's Elastic IP, so the internet sees the same address after the move: allowlists at your partners keep working. This page moves one Availability Zone at a time, with the [OpenTofu module](/docs/deploy/opentofu/) or [by hand](/docs/deploy/by-hand/).

An Elastic IP belongs to one network interface at a time, and a NAT gateway doesn't let go of its own while it exists. The NAT gateway must be deleted before natkit can take its address: egress from that zone stops from the deletion until natkit is up. Plan a window for each zone. If the address doesn't matter, [keep the NAT gateway until natkit routes](#without-the-same-address) instead: there is no window, only the switch of the routes.

## Before you start

- **One route table per zone.** Cutting over one zone at a time needs the private subnets of each zone on their own route table, pointing at that zone's NAT gateway. With one route table for every zone, all of them move at once.
- **The allocation IDs.** Note each NAT gateway's `eipalloc-...`, its zone and its route tables:

  ```sh
  aws ec2 describe-nat-gateways --filter Name=state,Values=available \
    --query 'NatGateways[].[NatGatewayId,SubnetId,NatGatewayAddresses[0].AllocationId,NatGatewayAddresses[0].PublicIp]' --output table
  aws ec2 describe-route-tables --filter Name=route.nat-gateway-id,Values=<nat-...> --query 'RouteTables[].RouteTableId'
  ```

- **Who manages the Elastic IPs.** If your IaC created them, take them out of its state before anything else, or deleting the NAT gateway there releases them for good. With the community [VPC module](/docs/deploy/vpc-module/), the addresses are its own unless it was given `reuse_nat_ips = true` and `external_nat_ip_ids`; its `aws_eip.nat` goes with `enable_nat_gateway = false`. Remove them from that state first:

  ```sh
  tofu state rm 'module.vpc.aws_eip.nat[0]'   # one per zone: [0], [1], ...
  ```

- **The instance type.** Size it from the NAT gateway's CloudWatch metrics (`BytesOutToDestination`, `ActiveConnectionCount`, `ConnectionEstablishedCount`) and [Performance](/docs/operate/performance/). Turn on the [allowance metrics](/docs/operate/monitoring/#allowance-metrics) (`conntrack_alarm_actions`), so you see the instance's network limits from the first day.
- **A test first.** Do one zone in a test VPC, and time the window there.

## One zone, with the module

For each zone, in turn:

1. **Write the zone's NAT.** One module per zone, in that zone's public subnet, on that zone's route tables, with `eip = true` and an `import` block that adopts the NAT gateway's Elastic IP instead of allocating a new one. Don't apply it yet.

   ```hcl
   module "nat_a" {
     source          = "github.com/ulm0/natkit//tofu/nat?ref=v0.1.7"
     name            = "nat-a"
     vpc_id          = aws_vpc.main.id
     subnet_id       = aws_subnet.public_a.id
     route_table_ids = [aws_route_table.private_a.id]
     ami_id          = data.aws_ami.natkit.id
   }

   import {
     to = module.nat_a.aws_eip.this[0]
     id = "eipalloc-0123456789abcdef0" # the NAT gateway's
   }
   ```

2. **Delete the NAT gateway**, and wait until it is gone. Its Elastic IP stays allocated, now associated with nothing. The window starts here.

   ```sh
   aws ec2 delete-nat-gateway --nat-gateway-id nat-...
   aws ec2 wait nat-gateway-deleted --nat-gateway-ids nat-...
   ```

3. **Delete its default routes.** A deleted NAT gateway leaves its routes behind as blackholes, and the module can't create its own `0.0.0.0/0` next to one. Remove it from each of the zone's route tables, or remove it from your IaC and apply that:

   ```sh
   aws ec2 delete-route --route-table-id rtb-... --destination-cidr-block 0.0.0.0/0
   ```

   If the NAT gateway was in your IaC, removing it there does steps 2 and 3 in one apply.

4. **Apply.** `tofu apply` imports the Elastic IP (adding the module's tags to it), creates the static ENI and its routes, and launches the instance. natkit associates the address with the instance at boot. The window ends when its [console](/docs/start/getting-started/#check-that-its-up) says `natkit: ready, ...` with the allocation ID after `eip`; `natkit: FAIL <reason>` says why not.
5. **Check.** From a client in that zone, `curl -s https://checkip.amazonaws.com` prints the NAT gateway's old address. Leave the zone a while, with the [alarms](/docs/operate/monitoring/#alarms) on, before you move the next one.

The `import` block has done its job once applied: remove it, or keep it, it changes nothing after.

## One zone, by hand

With an instance or an Auto Scaling group of your own, give natkit the allocation in its user-data, and let its [instance role](/docs/reference/instance-role/) associate it:

```yaml
eip_id: eipalloc-0123456789abcdef0
```

Then do steps 2 to 5 above: delete the NAT gateway, replace the zone's default routes with routes to the NAT instance's ENI (`aws ec2 replace-route ... --network-interface-id eni-...` on a blackhole route), and check.

## Without the same address

If nothing outside depends on the address, there is no window: natkit takes a new Elastic IP and starts routing before the NAT gateway goes.

1. Apply the zone's module without the `import` block and with `route_table_ids = []`: natkit comes up with a new Elastic IP and routes nothing yet.
2. Move the zone's traffic in one call per route table, to the module's `eni_id` output:

   ```sh
   aws ec2 replace-route --route-table-id rtb-... --destination-cidr-block 0.0.0.0/0 --network-interface-id eni-...
   ```

3. Hand the routes to the module. Take the NAT gateway's route out of your IaC without deleting it (`tofu state rm` first, if it is a resource of its own), set the module's `route_table_ids`, and import each route instead of creating it:

   ```hcl
   import {
     to = module.nat_a.aws_route.ipv4[0]
     id = "rtb-0123456789abcdef0_0.0.0.0/0"
   }
   ```

4. Check, then delete the NAT gateway and release its Elastic IP.

Connections open through the NAT gateway at the switch are cut, as in a [handover](/docs/how-it-works/high-availability/).

## Going back

Before the next zone, a zone can go back to a NAT gateway with the same address. It is the same window in reverse.

1. **Keep the address.** The module now manages the Elastic IP, and removing the module would release it. Take it out of the state first:

   ```sh
   tofu state rm 'module.nat_a.aws_eip.this[0]'
   ```

2. **Remove the module** from the configuration and apply. The instance terminates, which frees the address, and the module's routes go. The window starts here.
3. **Create the NAT gateway** with the same allocation, and wait for it:

   ```sh
   aws ec2 create-nat-gateway --subnet-id subnet-... --allocation-id eipalloc-0123456789abcdef0
   aws ec2 wait nat-gateway-available --nat-gateway-ids nat-...
   ```

4. **Route to it** again (`aws ec2 create-route --route-table-id rtb-... --destination-cidr-block 0.0.0.0/0 --nat-gateway-id nat-...`), or put both back in your IaC and import them. The window ends here.

Going back by hand is the same: terminate the instance (or delete its group), then steps 3 and 4.

What it costs on either side: see [Costs](/docs/operate/costs/#compared-with-a-nat-gateway).
