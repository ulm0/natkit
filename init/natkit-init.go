package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// natkit-init (onboot, every boot): fck-nat's setup (github.com/AndrewGuenther/fck-nat, service/fck-nat.sh) without
// systemd or the aws CLI, from the user-data (userdata.go). In order: nftables masquerade out of the public
// interface, forwarding and tuning sysctls, NAT64 (Jool, pool6 64:ff9b::/96), no more module loading, the SSM
// parameters, the lifecycle hook, and only then the static ENI and the EIP: an instance whose setup fails takes
// neither. With the ENI, it completes the lifecycle hook: the group puts the instance in service only once it routes.
// One result line goes to the serial console:
//
//	natkit: ready, public <if> private <if>, eip <id>|none, nat64 on|off, cwagent on|off
//	natkit: FAIL <reason>

const (
	runDir    = "/run/natkit"
	lockedRow = "root:*::0:99999:7:::\n" // the getty's shadow: root locked until console_password replaces it
)

type initRun struct {
	h        *host
	c        config
	ctx      context.Context
	md       metadata
	newCloud func(region string) cloud
	cl       cloud
	con      console
	u        *userData

	iid, mac, primaryENI, public, private string
	allowance                             uint64
	haveAllowance                         bool
	nat64, cwagent                        string
	hook, hookGroup                       string
}

// failure: why the setup stopped (die). runInit turns it into the console's FAIL line.
type failure struct{ msg string }

func runInit(h *host, c config) int {
	md := newIMDS(c.imds)
	r := &initRun{
		h:        h,
		c:        c,
		ctx:      context.Background(),
		md:       imdsMetadata{md},
		newCloud: func(region string) cloud { return newCloud(c, region, md) },
		con:      console{path: c.console},
	}
	return r.run()
}

// run: 0 once the NAT is ready, 1 after a FAIL line. A panic still leaves one: the console is the only place a
// failed boot shows.
func (r *initRun) run() (status int) {
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		status = 1
		if f, ok := v.(failure); ok {
			fmt.Fprintf(r.h.stderr, "natkit-init: %s\n", f.msg)
			r.result("FAIL " + f.msg)
		} else {
			fmt.Fprintf(r.h.stderr, "natkit-init: stopped: %v\n", v)
			r.result(fmt.Sprintf("FAIL natkit-init stopped: %v", v))
		}
		_ = r.h.write(runDir+"/failed", "", 0o644)
	}()
	r.setup()
	return 0
}

func (r *initRun) die(format string, a ...any) { panic(failure{fmt.Sprintf(format, a...)}) }
func (r *initRun) log(format string, a ...any) {
	fmt.Fprintf(r.h.stdout, "natkit-init: %s\n", fmt.Sprintf(format, a...))
}
func (r *initRun) result(s string) { r.con.write("natkit: " + s) }

func (r *initRun) setup() {
	h, u := r.h, (*userData)(nil)
	if err := os.MkdirAll(h.path(runDir), 0o755); err != nil {
		r.die("%s: %v", runDir, err)
	}
	_ = os.Remove(h.path(runDir + "/failed"))
	// the getty binds this file: it must exist even if the setup fails below, or there'd be no console to see why
	if err := h.write(runDir+"/getty.shadow", lockedRow, 0o600); err != nil {
		r.die("getty.shadow: %v", err)
	}

	// IMDSv2 (hop limit 1 is enough: this container shares the host network). Right after boot it can take a moment.
	var err error
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithTimeout(r.ctx, tokenTimeout)
		r.iid, err = r.md.get(ctx, "instance-id")
		cancel()
		if err == nil {
			break
		}
		h.sleep(time.Second)
	}
	if err != nil {
		r.die("no instance metadata service")
	}
	ud, err := r.md.userData(r.ctx)
	if err != nil {
		r.die("%v", err)
	}
	if len(ud) == 0 {
		ud = []byte("{}")
	}
	_ = h.write(runDir+"/user-data", string(ud), 0o600)
	if u, err = parseUserData(ud); err != nil {
		r.die("%v", err)
	}
	r.u = u

	region := r.meta(func() (string, error) { return r.md.region(r.ctx) })
	r.mac = r.meta(func() (string, error) { return r.md.get(r.ctx, "mac") })
	r.primaryENI = r.meta(func() (string, error) {
		return r.md.get(r.ctx, "network/interfaces/macs/"+r.mac+"/interface-id")
	})
	var ok bool
	if r.public, ok = r.ifname(r.mac); !ok {
		r.die("no interface with the primary ENI's MAC %s", r.mac)
	}
	r.private = r.public
	// the public interface: the primary ENI's, or user-data's `interface` (ignored with eni_id)
	if u.eniID == "" && u.iface != "" {
		r.public, r.private = u.iface, u.iface
		if !h.exists("/sys/class/net/" + u.iface) {
			r.die("interface: no interface %s", u.iface)
		}
	}
	r.cl = r.newCloud(region)

	if out, err := h.run(ruleset(r.public, u), "nft", "-f", "-"); err != nil {
		r.die("nftables: %s", firstLine(out))
	}
	clients := ""
	for _, c := range [][]string{u.clients4, u.clients6} {
		if len(c) > 0 {
			clients += ", clients " + strings.Join(c, ", ")
		}
	}
	r.log("masquerading out of %s%s", r.public, clients)

	r.sysctls()
	r.allowance, r.haveAllowance = r.readAllowance(r.public)
	r.tuning()
	if r.haveAllowance {
		cmax, _ := h.read("/proc/sys/net/netfilter/nf_conntrack_max")
		l := fmt.Sprintf("EC2 conntrack allowance %d, nf_conntrack_max %s", r.allowance, strings.TrimSpace(cmax))
		r.log("%s", l)
		r.con.write("natkit-init: " + l) // not as "natkit: ": that prefix is the one result line
	}
	// metrics: natkit-metrics (a service) publishes the allowance counters when this file, with the name, exists.
	// Written by inService: an instance that fails its setup doesn't route, and mustn't report under the NAT's name.
	_ = os.Remove(h.path(runDir + "/metrics"))
	if u.metrics != "" && !r.haveAllowance {
		r.die("metrics: no conntrack_allowance counters on %s (not an ENA interface?)", r.public)
	}

	r.setupNAT64()
	// nothing loads a module after this (drivers and netfilter are built in): no more kernel code until reboot.
	// Not under test/init.sh, where it would lock the docker host's kernel.
	if !r.c.testing {
		if err := h.write("/proc/sys/kernel/modules_disabled", "1\n", 0o644); err != nil {
			r.die("sysctl kernel.modules_disabled")
		}
	}
	r.setupCwagent()
	r.setupConsolePassword()
	r.findHook()
	r.takeENI()
	r.associateEIP()

	// rp_filter also per interface: the ENI attached just now has the defaults (conf/default), these make it
	// explicit. A router on one interface would tell clients to bypass it (ICMP redirects); the kernel ORs all with
	// each interface.
	for _, f := range append(h.glob("/proc/sys/net/ipv4/conf/*/rp_filter"), h.glob("/proc/sys/net/ipv4/conf/*/send_redirects")...) {
		if err := h.write(f, "0\n", 0o644); err != nil {
			r.die("sysctl %s=0", strings.ReplaceAll(strings.TrimPrefix(f, "/proc/sys/"), "/", "."))
		}
	}
	r.inService() // without a static ENI: now that all of it worked

	eip := u.eipID
	if eip == "" {
		eip = "none"
	}
	r.result(fmt.Sprintf("ready, public %s private %s, eip %s, nat64 %s, cwagent %s", r.public, r.private, eip, r.nat64, r.cwagent))
	r.log("done")
}

func (r *initRun) meta(get func() (string, error)) string {
	v, err := get()
	if err != nil {
		r.die("%v", err)
	}
	return v
}

// ifname: the interface with this MAC (no udev here: no ENI-ID altnames as on Amazon Linux)
func (r *initRun) ifname(mac string) (string, bool) {
	for _, d := range r.h.glob("/sys/class/net/*") {
		if a, err := r.h.read(d + "/address"); err == nil && strings.TrimSpace(a) == mac {
			return path.Base(d), true
		}
	}
	return "", false
}

// eniMAC: the MAC of an ENI attached to this instance
func (r *initRun) eniMAC(eni string) (string, bool) {
	list, err := r.md.get(r.ctx, "network/interfaces/macs/")
	if err != nil {
		return "", false
	}
	for _, m := range strings.Fields(list) {
		m = strings.TrimSuffix(m, "/")
		if id, err := r.md.get(r.ctx, "network/interfaces/macs/"+m+"/interface-id"); err == nil && id == eni {
			return m, true
		}
	}
	return "", false
}

// sysctls: forwarding, rp_filter and hardening (/etc/natkit/sysctl.conf, one `key = value` a line). A key that
// doesn't take is a failed boot, not a NAT that quietly doesn't forward. Under test/init.sh only the net.* keys
// (the container's own): the rest are the docker host's kernel.
func (r *initRun) sysctls() {
	conf, err := r.h.read("/etc/natkit/sysctl.conf")
	if err != nil {
		r.die("sysctl: %v", err)
	}
	for _, line := range strings.Split(conf, "\n") {
		k, v, _ := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" || strings.HasPrefix(k, "#") || (r.c.testing && !strings.HasPrefix(k, "net.")) {
			continue
		}
		if err := r.h.write("/proc/sys/"+strings.ReplaceAll(k, ".", "/"), v+"\n", 0o644); err != nil {
			r.die("sysctl %s=%s", k, v)
		}
	}
}

// readAllowance: EC2's connection-tracking allowance for this instance type (the ENA driver's counter): flows past
// it are dropped before they reach natkit. Read before the ENI brings the clients' traffic, so it's the whole
// allowance.
func (r *initRun) readAllowance(iface string) (uint64, bool) {
	out, err := r.h.run("", "ethtool", "-S", iface)
	if err != nil {
		return 0, false
	}
	c := parseEthtool(out)
	return c.available, c.haveAvailable
}

// tuning, as fck-nat: only what user-data sets, except the conntrack table. The kernel sizes it for a host (about
// 7 000 flows under 1 GiB, 65 536 up to 4 GiB) and a NAT that fills it drops every new connection ("nf_conntrack:
// table full"). Default: one flow per 8 kB of RAM (a full table is about 4% of memory), and at least EC2's allowance:
// below it the kernel's table fills first, where the allowance metrics don't see it (a t4g.nano: 55 000 flows
// against 154 000). An entry takes about 400 bytes: 154 000 are about 60 MB. A quarter as many buckets.
// Not under test/init.sh: the table is the docker host's.
func (r *initRun) tuning() {
	u := r.u
	cmax, buckets := u.conntrackMax, u.conntrackBuckets
	if !r.c.testing {
		if cmax == "" {
			cmax = strconv.FormatUint(conntrackDefault(r.memTotalKB(), r.allowance, r.haveAllowance), 10)
		}
		if buckets == "" {
			n, _ := strconv.ParseUint(cmax, 10, 64)
			buckets = strconv.FormatUint(n/4, 10)
		}
	}
	for _, s := range []struct{ key, value string }{
		{"net.ipv4.ip_local_port_range", u.portRange},
		{"net.netfilter.nf_conntrack_max", cmax},
		{"net.netfilter.nf_conntrack_tcp_timeout_established", u.tcpEstablished},
		{"net.ipv4.tcp_keepalive_time", u.keepaliveTime},
		{"net.ipv4.tcp_max_syn_backlog", u.synBacklog},
	} {
		if s.value == "" {
			continue
		}
		if err := r.h.write("/proc/sys/"+strings.ReplaceAll(s.key, ".", "/"), s.value+"\n", 0o644); err != nil {
			r.die("sysctl %s=%s", s.key, s.value)
		}
	}
	if buckets != "" {
		if err := r.h.write("/sys/module/nf_conntrack/parameters/hashsize", buckets+"\n", 0o644); err != nil {
			r.die("nf_conntrack_buckets=%s", buckets)
		}
	}
}

// conntrackDefault: one flow per 8 kB of memory, and at least EC2's allowance
func conntrackDefault(memKB, allowance uint64, haveAllowance bool) uint64 {
	n := memKB / 8
	if haveAllowance && allowance > n {
		n = allowance
	}
	return n
}

func (r *initRun) memTotalKB() uint64 {
	m, _ := r.h.read("/proc/meminfo")
	for _, line := range strings.Split(m, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "MemTotal:" {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			return n
		}
	}
	return 0
}

// setupNAT64: Jool's modules (the jool image in natkit.yml's init) and its userspace tool (this image), same version.
// With client_cidrs, address-dependent filtering: by default Jool lets any IPv4 host reach a client's mapping
// (pool4 address and port); with it, only the hosts the client contacted, as the IPv4 masquerade does. As in
// fck-nat, a NAT64 that fails is skipped: the IPv4 NAT works without it.
func (r *initRun) setupNAT64() {
	r.nat64 = "off"
	rel := r.h.release()
	dir := "/lib/modules/" + rel + "/extra"
	switch {
	case !r.u.nat64:
		r.log("NAT64: off in user-data")
	case !r.h.exists(dir + "/jool.ko"):
		r.log("NAT64: no Jool modules for %s, skipped", rel)
	default:
		if err := r.startJool(dir); err != nil {
			r.log("NAT64: Jool failed, skipped: %v", err)
			return
		}
		r.nat64 = "on"
		r.log("NAT64 on, pool6 %s", joolPool6)
	}
}

func (r *initRun) startJool(dir string) error {
	loaded, _ := r.h.read("/proc/modules")
	for _, m := range []string{"jool_common", "jool"} {
		if strings.HasPrefix(loaded, m+" ") || strings.Contains(loaded, "\n"+m+" ") {
			continue
		}
		if err := r.h.loadModule(r.h.path(dir + "/" + m + ".ko")); err != nil {
			return fmt.Errorf("insmod %s: %w", m, err)
		}
	}
	cmds := [][]string{{"instance", "flush"}, {"instance", "add", "--netfilter", "--pool6", joolPool6}}
	if r.u.clients {
		cmds = append(cmds, []string{"global", "update", "address-dependent-filtering", "true"})
	}
	for _, c := range cmds {
		if _, err := r.h.run("", "jool", c...); err != nil {
			return err
		}
	}
	return nil
}

// setupCwagent: natkit-cwagent (a service) runs the CloudWatch agent when this config exists
func (r *initRun) setupCwagent() {
	r.cwagent = "off"
	dir := runDir + "/cwagent"
	_ = os.RemoveAll(r.h.path(dir))
	if !r.u.cwagentEnabled {
		return
	}
	p := r.u.cwagentParam
	if p == "" {
		r.die("cwagent_enabled needs cwagent_cfg_param_name")
	}
	if err := os.MkdirAll(r.h.path(dir), 0o755); err != nil {
		r.die("cwagent: %v", err)
	}
	v := r.ssm(p, "cwagent_cfg_param_name")
	if err := r.h.write(dir+"/cwagentconfig.json", v+"\n", 0o644); err != nil {
		r.die("cwagent: %v", err)
	}
	var obj map[string]any
	if json.Unmarshal([]byte(v), &obj) != nil {
		r.die("cwagent: %s is not a JSON object", p)
	}
	r.cwagent = "on"
}

// setupConsolePassword: serial console root login (getty): a crypt(3) hash from SSM (openssl passwd -6), else root
// stays locked. An empty lastchg: 0 would force a password change.
func (r *initRun) setupConsolePassword() {
	p := r.u.consolePassword
	if p == "" {
		return
	}
	hash := r.ssm(p, "console_password")
	if !strings.HasPrefix(hash, "$") {
		r.die("console_password: %s is not a crypt hash (openssl passwd -6)", p)
	}
	if err := r.h.write(runDir+"/getty.shadow", "root:"+hash+"::0:99999:7:::\n", 0o600); err != nil {
		r.die("getty.shadow: %v", err)
	}
}

// ssm: a parameter's decrypted value, 10 tries 3 s apart
func (r *initRun) ssm(name, what string) string {
	var v string
	var err error
	for i := 1; ; i++ {
		if v, err = r.cl.parameter(r.ctx, name); err == nil {
			return v
		}
		if i >= 10 {
			r.die("ssm: can't read %s for %s: %v", name, what, err)
		}
		r.h.sleep(3 * time.Second)
	}
}

// retry: a call tried <tries> times, 3 s apart (the instance role can take a few seconds to work after launch);
// the last error goes on the console with the failure
func (r *initRun) retry(tries int, what string, call func() error) {
	for i := 1; ; i++ {
		err := call()
		if err == nil {
			return
		}
		if i >= tries {
			r.die("%s failed: natkit-init: %v", what, err)
		}
		r.h.sleep(3 * time.Second)
	}
}

// findHook: a launch lifecycle hook of the Auto Scaling group keeps this instance out of service (Pending:Wait)
// until natkit completes it, once it has the ENI. The group terminates an instance that never does (a setup that
// failed here, an image that didn't boot) and tries again, and until one completes it keeps the old instance, the
// one that routes. Without the hook the group only waits for EC2 to call the instance running, and then terminates
// the old one.
// Found now, before the ENI: an instance that can't reach its hook could never be put in service, and must not take
// the ENI from the one that is. The heartbeat proves the hook's name and the permission, and restarts its timeout.
func (r *initRun) findHook() {
	r.hook = r.u.lifecycleHook
	if r.hook == "" {
		return
	}
	var group, state string
	for w := 1; ; w++ {
		r.retry(10, "DescribeAutoScalingInstances", func() (err error) {
			group, state, err = r.cl.asgInstance(r.ctx, r.iid)
			return err
		})
		if state != "Pending" { // Pending: the group hasn't got to its hooks yet
			break
		}
		if w >= 100 {
			r.die("lifecycle_hook: still Pending in the Auto Scaling group after 5 min")
		}
		r.h.sleep(3 * time.Second)
	}
	if state != "Pending:Wait" {
		// launched before the group had the hook, a reboot, or an instance of no group
		if state == "" {
			state = "in no Auto Scaling group"
		}
		r.log("lifecycle_hook: nothing to complete, the instance is %s", state)
		return
	}
	r.hookGroup = group
	r.retry(10, "RecordLifecycleActionHeartbeat "+r.hook, func() error { return r.cl.heartbeat(r.ctx, group, r.hook, r.iid) })
	r.log("%s holds this instance out of service until it completes %s", group, r.hook)
}

// inService: completes the hook, once. A failure here leaves an instance that routes but that the group will
// replace when the hook times out: the console says why.
func (r *initRun) inService() {
	if r.u.metrics != "" {
		_ = r.h.write(runDir+"/metrics", r.u.metrics+"\n", 0o644)
	}
	if r.hookGroup == "" {
		return
	}
	r.retry(10, "CompleteLifecycleAction "+r.hook, func() error { return r.cl.complete(r.ctx, r.hookGroup, r.hook, r.iid) })
	r.log("completed %s: in service", r.hook)
	r.hookGroup = ""
}

// takeENI: the static ENI, after everything that can fail has not, so an instance whose setup failed never takes
// the route tables' traffic.
func (r *initRun) takeENI() {
	eni := r.u.eniID
	if eni == "" {
		return
	}
	r.log("turning off %s's source/dest check", r.primaryENI)
	r.retry(10, "ModifyNetworkInterfaceAttribute "+r.primaryENI, func() error { return r.cl.sourceDestCheckOff(r.ctx, r.primaryENI) })
	if _, attached := r.eniMAC(eni); !attached {
		r.takeOver(eni)
		r.attach(eni)
	}
	for i := 1; ; i++ {
		if m, ok := r.eniMAC(eni); ok {
			if p, ok := r.ifname(m); ok {
				r.private = p
				break
			}
		}
		if i >= 120 {
			r.die("%s attached, but no interface for it after 2 min", eni)
		}
		r.h.sleep(time.Second)
	}
	// its address comes from the dhcpcd service; forwarding doesn't need one
	if err := r.h.linkUp(r.private); err != nil {
		r.die("ip link set %s up: %v", r.private, err)
	}
	r.log("%s is %s", eni, r.private)
	// With the ENI, this instance is the one that routes, whatever happens next: in service now, not after the EIP.
	// The group must not take a failed AssociateAddress for a reason to terminate it and keep the old instance,
	// which no longer has the ENI.
	r.inService()
}

// takeOver: an ASG replacement, where the old instance still holds the ENI. A forced detach (the old instance is on
// its way out and needn't agree), so egress stops for the detach and attach only, not for the old instance's
// shutdown or this one's boot. The lookup and the detach are tried 10 times, as the other calls: throttling, a
// request that times out, a detach still in progress. Then, held by the lifecycle hook, a failure: waiting is no
// use, the group doesn't terminate the old instance before this one is in service. Without the hook, wait for the
// ENI, as fck-nat does.
func (r *initRun) takeOver(eni string) {
	for i := 1; ; i++ {
		att, err := r.cl.attachment(r.ctx, eni)
		if err == nil {
			if att == "" {
				return
			}
			r.log("taking %s over from its instance (%s)", eni, att)
			if err = r.cl.detach(r.ctx, att); err == nil {
				return
			}
		}
		if i >= 10 {
			if r.hookGroup != "" {
				r.die("%s: can't take it over, and its instance stays until this one is in service (lifecycle_hook): natkit-init: %v", eni, err)
			}
			r.log("can't take %s over, waiting for it: natkit-init: %v", eni, err)
			return
		}
		r.h.sleep(3 * time.Second)
	}
}

// attach: until the old instance lets go of it, for up to 15 min (fck-nat waits forever; natkit gives up so the
// console shows the failure)
func (r *initRun) attach(eni string) {
	r.log("attaching %s", eni)
	for i := 1; ; i++ {
		err := r.cl.attach(r.ctx, r.iid, eni)
		if err == nil {
			return
		}
		if i >= 450 {
			r.die("AttachNetworkInterface %s: still failing after 15 min: natkit-init: %v", eni, err)
		}
		// the first refusal on the console: a wrong eni_id or a missing permission would otherwise show nothing
		// there for 15 min. (In a handover it's the old instance's detach, still in progress.)
		if i == 1 {
			r.con.write(fmt.Sprintf("natkit-init: waiting for %s: natkit-init: %v", eni, err))
		}
		if i%15 == 1 {
			r.log("waiting for %s to attach: natkit-init: %v", eni, err)
		}
		r.h.sleep(2 * time.Second)
	}
}

// associateEIP, last (fck-nat takes it first): a replacement instance waiting for the static ENI must not pull the
// EIP off the old instance, which is still the one routing
func (r *initRun) associateEIP() {
	eip := r.u.eipID
	if eip == "" {
		return
	}
	r.log("associating %s with %s", eip, r.primaryENI)
	r.retry(10, "AssociateAddress "+eip, func() error { return r.cl.associateAddress(r.ctx, eip, r.primaryENI) })
}
