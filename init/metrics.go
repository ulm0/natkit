package main

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// natkit-metrics (service): EC2's connection-tracking allowance counters to CloudWatch, only when natkit-init wrote
// its config (user-data metrics). The ENA driver has them (ethtool -S): two numbers and one PutMetricData a cycle,
// so no agent, and it runs on 0.5 GiB. Namespace natkit, dimension Name=<metrics>:
//
//	conntrack_allowance_exceeded   packets EC2 dropped because the allowance ran out, since the last report: on any
//	                               interface (EC2 counts a drop on the ENI the packet was on). Alarm on Sum > 0.
//	conntrack_allowance_available  tracked connections left before it runs out (the instance's, the same on every ENI)
//
// The first reading is where the counting starts: its report says 0 dropped, if it gets through. A report that fails
// is not lost: the next one that gets through covers the time since the last one that did. That matters here,
// because a report is a new connection too, and EC2 drops it like any other while the allowance is exhausted.

const metricsInterval = 50 * time.Second // a cycle stays under a minute: no 1-minute period without a datapoint

// counters: the ENA driver's conntrack allowance counters, summed (exceeded) or the lowest (available) over the
// interfaces
type counters struct {
	exceeded      uint64
	available     uint64
	haveExceeded  bool
	haveAvailable bool
}

// parseEthtool: the counters in `ethtool -S` output (one interface's)
func parseEthtool(out string) counters {
	var c counters
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "conntrack_allowance_exceeded:":
			c.exceeded += n
			c.haveExceeded = true
		case "conntrack_allowance_available:":
			if !c.haveAvailable || n < c.available {
				c.available = n
			}
			c.haveAvailable = true
		}
	}
	return c
}

// readCounters: the counters of every interface, added up
func readCounters(h *host) counters {
	var all counters
	for _, d := range h.glob("/sys/class/net/*") {
		out, err := h.run("", "ethtool", "-S", path.Base(d))
		if err != nil {
			continue
		}
		c := parseEthtool(out)
		if c.haveExceeded {
			all.exceeded += c.exceeded
			all.haveExceeded = true
		}
		if c.haveAvailable && (!all.haveAvailable || c.available < all.available) {
			all.available = c.available
			all.haveAvailable = true
		}
	}
	return all
}

// dropCounter: the packets dropped since the last report that got through
type dropCounter struct {
	last    uint64
	started bool
}

// since: the drops to report for this reading. The first reading, or one lower than the last report's (an interface
// went: its ENI was taken over), is where the counting starts.
func (d *dropCounter) since(sum uint64) uint64 {
	if !d.started || sum < d.last {
		d.last, d.started = sum, true
	}
	return sum - d.last
}

// reported: the report with this reading got through
func (d *dropCounter) reported(sum uint64) { d.last = sum }

type publisher struct {
	h        *host
	md       metadata
	newCloud func(region string) cloud
	cl       cloud
	drops    dropCounter
}

// publish: one report; "" once it got through, else why not
func (p *publisher) publish(ctx context.Context, name string) string {
	// the counters first: a cycle that can't reach IMDS (the allowance exhausted) still sets where the counting starts
	c := readCounters(p.h)
	if !c.haveExceeded || !c.haveAvailable {
		return "no conntrack_allowance counters on any interface"
	}
	dropped := p.drops.since(c.exceeded)
	if p.cl == nil {
		ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
		region, err := p.md.region(ctx)
		cancel()
		if err != nil {
			return err.Error()
		}
		p.cl = p.newCloud(region)
	}
	if err := p.cl.putMetrics(ctx, name, float64(dropped), float64(c.available)); err != nil {
		return err.Error()
	}
	p.drops.reported(c.exceeded)
	return ""
}

func runMetrics(h *host, c config) int {
	md := newIMDS(c.imds)
	p := &publisher{h: h, md: imdsMetadata{md}, newCloud: func(region string) cloud { return newCloud(c, region, md) }}
	return metricsLoop(h, c, p)
}

// metricsLoop: says on the console when it starts to work and when it stops, not every cycle
func metricsLoop(h *host, c config, p *publisher) int {
	con := console{path: c.console, append: true}
	say := func(s string) {
		con.write("natkit-metrics: " + s)
		fmt.Fprintln(h.stderr, "natkit-metrics: "+s)
	}
	b, err := h.read(runDir + "/metrics")
	if err != nil {
		fmt.Fprintln(h.stdout, "natkit-metrics: not enabled")
		return 0
	}
	name := strings.TrimSpace(b)
	if name == "" || strings.ContainsAny(name, " \n") {
		say("bad " + runDir + "/metrics")
		return 1
	}
	ok := -1
	for n := 1; ; n++ {
		if why := p.publish(context.Background(), name); why == "" {
			if ok != 1 {
				say("publishing to CloudWatch, namespace " + metricsName + ", Name=" + name)
			}
			ok = 1
		} else {
			if ok != 0 {
				say("can't publish: " + why)
			}
			ok = 0
		}
		if c.metricsCycles > 0 { // test/init.sh: that many cycles, without the wait
			if n >= c.metricsCycles {
				return 1 - ok
			}
			continue
		}
		h.sleep(metricsInterval)
	}
}
