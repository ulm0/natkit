package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type initCase struct {
	h      *fakeHost
	md     *fakeMetadata
	cl     *fakeCloud
	status int
}

// runInitWith: natkit-init against the fakes; testMode as under test/init.sh (the kernel isn't ours to tune)
func runInitWith(t *testing.T, ud string, testMode bool, tweak func(*initCase)) *initCase {
	t.Helper()
	c := &initCase{h: newFakeHost(t)}
	c.md = newFakeMetadata(ud)
	c.cl = newFakeCloud(c.md)
	if tweak != nil {
		tweak(c)
	}
	r := &initRun{
		h:        c.h.host,
		c:        config{console: c.h.path("/console"), testing: testMode},
		ctx:      context.Background(),
		md:       c.md,
		newCloud: func(region string) cloud { return c.cl },
		con:      console{path: c.h.path("/console")},
	}
	c.status = r.run()
	return c
}

func (c *initCase) console() string { return strings.TrimSpace(c.h.file("/console")) }

func (c *initCase) wantReady(t *testing.T, line string) {
	t.Helper()
	if c.status != 0 || c.console() != "natkit: "+line {
		t.Fatalf("status %d, console %q, want %q\nstdout:\n%s\nstderr:\n%s", c.status, c.console(), line, c.h.out.String(), c.h.err.String())
	}
	if c.h.file(runDir+"/failed") == "" {
		t.Error("ready, but a failed marker")
	}
}

func (c *initCase) wantFail(t *testing.T, reason string) {
	t.Helper()
	if c.status != 1 || !strings.HasPrefix(c.console(), "natkit: FAIL "+reason) {
		t.Fatalf("status %d, console %q, want FAIL %q\nstdout:\n%s", c.status, c.console(), reason, c.h.out.String())
	}
	if c.h.file(runDir+"/failed") != "" {
		t.Error("no failed marker after a FAIL")
	}
}

func TestInitPlainNAT(t *testing.T) {
	c := runInitWith(t, "", true, nil)
	c.wantReady(t, "ready, public eth0 private eth0, eip none, nat64 off, cwagent off")
	if len(c.cl.calls) != 0 {
		t.Errorf("AWS calls without user-data: %v", c.cl.calls)
	}
	if got := c.h.commands; len(got) < 1 || got[0] != "nft -f -" {
		t.Errorf("commands %v", got)
	}
	if got := c.h.file(runDir + "/getty.shadow"); got != lockedRow {
		t.Errorf("getty.shadow %q", got)
	}
	if fi, err := os.Stat(c.h.path(runDir + "/getty.shadow")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("getty.shadow mode %v %v", fi.Mode(), err)
	}
	if got := c.h.file("/proc/sys/net/ipv4/ip_forward"); got != "1\n" {
		t.Errorf("ip_forward %q", got)
	}
	if got := c.h.file("/proc/sys/kernel/dmesg_restrict"); !strings.HasPrefix(got, "<") {
		t.Error("a kernel.* sysctl under test/init.sh (the docker host's kernel)")
	}
	if got := c.h.file("/proc/sys/kernel/modules_disabled"); !strings.HasPrefix(got, "<") {
		t.Error("modules_disabled under test/init.sh: it would lock the docker host's kernel")
	}
	for _, f := range []string{"all/rp_filter", "eth0/rp_filter", "default/send_redirects", "eth0/send_redirects"} {
		if got := c.h.file("/proc/sys/net/ipv4/conf/" + f); got != "0\n" {
			t.Errorf("%s %q", f, got)
		}
	}
	if !strings.Contains(c.h.out.String(), "natkit-init: NAT64: no Jool modules for 6.18.63-linuxkit, skipped") {
		t.Errorf("log:\n%s", c.h.out.String())
	}
}

func TestInitEverything(t *testing.T) {
	c := runInitWith(t, `eip_id: eipalloc-0123
eni_id: eni-0bbbbbbbbbbbbbbbb
ip_local_port_range: "2048 60000"
nf_conntrack_tcp_timeout_established: 3600
tcp_keepalive_time: 600
tcp_max_syn_backlog: 4096
cwagent_enabled: true
cwagent_cfg_param_name: /natkit/test/cw
console_password: /natkit/test/root-hash
lifecycle_hook: natkit-ready`, true, func(c *initCase) {
		c.h.mkfile("/sys/class/net/eth9/address", testStaticMAC+"\n")
		c.cl.attachErrs = []error{errors.New("AttachNetworkInterface: HTTP 400 InvalidNetworkInterface.InUse: attached to i-old")}
	})
	c.wantReady(t, "ready, public eth0 private eth9, eip eipalloc-0123, nat64 on, cwagent on"[:0]+
		"ready, public eth0 private eth9, eip eipalloc-0123, nat64 off, cwagent on")
	want := []string{
		"GetParameter /natkit/test/cw",
		"GetParameter /natkit/test/root-hash",
		"DescribeAutoScalingInstances " + testIID,
		"DescribeAutoScalingInstances " + testIID,
		"RecordLifecycleActionHeartbeat natkit-test natkit-ready " + testIID,
		"ModifyNetworkInterfaceAttribute " + testENI,
		"DescribeNetworkInterfaces " + testStaticENI,
		"DetachNetworkInterface eni-attach-0ccccccccccccccc",
		"AttachNetworkInterface " + testIID + " " + testStaticENI,
		"AttachNetworkInterface " + testIID + " " + testStaticENI,
		"CompleteLifecycleAction natkit-test natkit-ready " + testIID,
		"AssociateAddress eipalloc-0123 " + testENI,
	}
	if !reflect.DeepEqual(c.cl.calls, want) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(c.cl.calls, "\n"), strings.Join(want, "\n"))
	}
	for f, v := range map[string]string{
		"/proc/sys/net/ipv4/ip_local_port_range":                       "2048 60000\n",
		"/proc/sys/net/netfilter/nf_conntrack_tcp_timeout_established": "3600\n",
		"/proc/sys/net/ipv4/tcp_keepalive_time":                        "600\n",
		"/proc/sys/net/ipv4/tcp_max_syn_backlog":                       "4096\n",
		runDir + "/getty.shadow":                                       "root:$6$salt$hash::0:99999:7:::\n",
		runDir + "/cwagent/cwagentconfig.json":                         `{"metrics": {"namespace": "natkit"}}` + "\n",
	} {
		if got := c.h.file(f); got != v {
			t.Errorf("%s: %q, want %q", f, got, v)
		}
	}
	if !reflect.DeepEqual(c.h.linkedUp, []string{"eth9"}) {
		t.Errorf("links up %v", c.h.linkedUp)
	}
	if !strings.Contains(c.h.out.String(), "natkit-test holds this instance out of service until it completes natkit-ready") {
		t.Errorf("log:\n%s", c.h.out.String())
	}
}

// Regression, v0.1.4's amd64 boot test: without the lifecycle hook, the takeover's one lookup timed out, natkit
// never detached the ENI and waited 15 min for an instance that wasn't going anywhere.
func TestInitTakeoverRetriedWithoutHook(t *testing.T) {
	c := runInitWith(t, "eni_id: eni-0bbbbbbbbbbbbbbbb", true, func(c *initCase) {
		c.h.mkfile("/sys/class/net/eth9/address", testStaticMAC+"\n")
		c.cl.attachmentErrs = []error{errors.New("DescribeNetworkInterfaces: Post \"https://ec2.us-east-1.amazonaws.com/\": context deadline exceeded")}
	})
	c.wantReady(t, "ready, public eth0 private eth9, eip none, nat64 off, cwagent off")
	want := []string{"ModifyNetworkInterfaceAttribute", "DescribeNetworkInterfaces", "DescribeNetworkInterfaces", "DetachNetworkInterface", "AttachNetworkInterface"}
	if !reflect.DeepEqual(c.cl.actions(), want) {
		t.Errorf("calls %v, want %v", c.cl.actions(), want)
	}
}

func TestInitTakeoverRefused(t *testing.T) {
	denied := errors.New("DescribeNetworkInterfaces: HTTP 403 UnauthorizedOperation: not authorized")
	t.Run("without the hook: waits for the ENI", func(t *testing.T) {
		c := runInitWith(t, "eni_id: eni-0bbbbbbbbbbbbbbbb", true, func(c *initCase) {
			c.h.mkfile("/sys/class/net/eth9/address", testStaticMAC+"\n")
			c.cl.attachmentErrs = make([]error, 10)
			for i := range c.cl.attachmentErrs {
				c.cl.attachmentErrs[i] = denied
			}
			c.cl.attachErrs = []error{errors.New("AttachNetworkInterface: HTTP 400 InvalidNetworkInterface.InUse: in use")}
		})
		c.wantReady(t, "ready, public eth0 private eth9, eip none, nat64 off, cwagent off")
		if n := strings.Count(strings.Join(c.cl.calls, "\n"), "DescribeNetworkInterfaces"); n != 10 {
			t.Errorf("%d lookups, want 10", n)
		}
		if strings.Contains(strings.Join(c.cl.calls, "\n"), "Detach") {
			t.Error("detached without a lookup")
		}
		if !strings.Contains(c.h.out.String(), "can't take eni-0bbbbbbbbbbbbbbbb over, waiting for it: natkit-init: DescribeNetworkInterfaces: HTTP 403") {
			t.Errorf("log:\n%s", c.h.out.String())
		}
	})
	t.Run("held by the hook: a failure before the attach", func(t *testing.T) {
		c := runInitWith(t, "eni_id: eni-0bbbbbbbbbbbbbbbb\nlifecycle_hook: natkit-ready", true, func(c *initCase) {
			c.h.mkfile("/sys/class/net/eth9/address", testStaticMAC+"\n")
			c.cl.attachmentErrs = make([]error, 10)
			for i := range c.cl.attachmentErrs {
				c.cl.attachmentErrs[i] = denied
			}
		})
		c.wantFail(t, "eni-0bbbbbbbbbbbbbbbb: can't take it over, and its instance stays until this one is in service (lifecycle_hook): natkit-init: DescribeNetworkInterfaces: HTTP 403 UnauthorizedOperation: not authorized")
		for _, a := range c.cl.actions() {
			if a == "AttachNetworkInterface" || a == "CompleteLifecycleAction" {
				t.Errorf("%s after a failed takeover", a)
			}
		}
	})
	t.Run("a throttled lookup and detach are tried again", func(t *testing.T) {
		c := runInitWith(t, "eni_id: eni-0bbbbbbbbbbbbbbbb", true, func(c *initCase) {
			c.h.mkfile("/sys/class/net/eth9/address", testStaticMAC+"\n")
			c.cl.attachmentErrs = []error{errors.New("DescribeNetworkInterfaces: HTTP 503 RequestLimitExceeded: Request limit exceeded.")}
			c.cl.detachErrs = []error{errors.New("DetachNetworkInterface: HTTP 503 RequestLimitExceeded: Request limit exceeded.")}
		})
		c.wantReady(t, "ready, public eth0 private eth9, eip none, nat64 off, cwagent off")
		want := []string{"ModifyNetworkInterfaceAttribute", "DescribeNetworkInterfaces", "DescribeNetworkInterfaces", "DetachNetworkInterface", "DescribeNetworkInterfaces", "DetachNetworkInterface", "AttachNetworkInterface"}
		if !reflect.DeepEqual(c.cl.actions(), want) {
			t.Errorf("calls %v, want %v", c.cl.actions(), want)
		}
	})
}

func TestInitAttach(t *testing.T) {
	t.Run("the first refusal on the console", func(t *testing.T) {
		c := runInitWith(t, "eni_id: eni-0bbbbbbbbbbbbbbbb", true, func(c *initCase) {
			c.h.mkfile("/sys/class/net/eth9/address", testStaticMAC+"\n")
			c.cl.attachErrs = []error{errors.New("AttachNetworkInterface: HTTP 400 InvalidParameterValue: in use")}
			c.h.answer = func(name string, args []string, _ string) (string, error) { return "", nil }
		})
		c.wantReady(t, "ready, public eth0 private eth9, eip none, nat64 off, cwagent off")
		if c.h.slept != 2*time.Second {
			t.Errorf("slept %v, want one 2 s wait", c.h.slept)
		}
	})
	t.Run("15 min of refusals is a failure", func(t *testing.T) {
		c := runInitWith(t, "eni_id: eni-0bbbbbbbbbbbbbbbb", true, func(c *initCase) {
			c.cl.attachErrs = make([]error, 450)
			for i := range c.cl.attachErrs {
				c.cl.attachErrs[i] = errors.New("AttachNetworkInterface: HTTP 403 UnauthorizedOperation: no")
			}
		})
		c.wantFail(t, "AttachNetworkInterface eni-0bbbbbbbbbbbbbbbb: still failing after 15 min: natkit-init: AttachNetworkInterface: HTTP 403 UnauthorizedOperation: no")
		if c.h.slept < 14*time.Minute {
			t.Errorf("gave up after %v", c.h.slept)
		}
	})
	t.Run("an ENI already attached (a reboot): no takeover", func(t *testing.T) {
		c := runInitWith(t, "eni_id: eni-0bbbbbbbbbbbbbbbb", true, func(c *initCase) {
			c.h.mkfile("/sys/class/net/eth9/address", testStaticMAC+"\n")
			*c.md.attached = true
		})
		c.wantReady(t, "ready, public eth0 private eth9, eip none, nat64 off, cwagent off")
		if !reflect.DeepEqual(c.cl.actions(), []string{"ModifyNetworkInterfaceAttribute"}) {
			t.Errorf("calls %v", c.cl.calls)
		}
	})
	t.Run("attached, but no interface for it", func(t *testing.T) {
		c := runInitWith(t, "eni_id: eni-0bbbbbbbbbbbbbbbb", true, nil)
		c.wantFail(t, "eni-0bbbbbbbbbbbbbbbb attached, but no interface for it after 2 min")
	})
}

func TestInitLifecycleHook(t *testing.T) {
	for _, tc := range []struct {
		name, ud string
		states   []string
		log      string
	}{
		{"in service already (a reboot)", "lifecycle_hook: natkit-ready", []string{"InService"}, "lifecycle_hook: nothing to complete, the instance is InService"},
		{"in no group", "lifecycle_hook: natkit-ready", []string{"none"}, "lifecycle_hook: nothing to complete, the instance is in no Auto Scaling group"},
		{"held, completed once ready", "lifecycle_hook: natkit-ready", []string{"Pending", "Pending", "Pending:Wait"}, "completed natkit-ready: in service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := runInitWith(t, tc.ud, true, func(c *initCase) { c.cl.asgStates = tc.states })
			c.wantReady(t, "ready, public eth0 private eth0, eip none, nat64 off, cwagent off")
			if !strings.Contains(c.h.out.String(), tc.log) {
				t.Errorf("log:\n%s", c.h.out.String())
			}
			heartbeat := strings.Contains(strings.Join(c.cl.calls, "\n"), "RecordLifecycleActionHeartbeat")
			if heartbeat != (tc.states[len(tc.states)-1] == "Pending:Wait") {
				t.Errorf("calls %v", c.cl.calls)
			}
		})
	}
	t.Run("a hook it can't reach fails before the ENI", func(t *testing.T) {
		c := runInitWith(t, "eni_id: eni-0bbbbbbbbbbbbbbbb\nlifecycle_hook: other", true, nil)
		c.wantFail(t, "RecordLifecycleActionHeartbeat other failed: natkit-init: RecordLifecycleActionHeartbeat: HTTP 400 ValidationError: No active Lifecycle Action found")
		for _, a := range c.cl.actions() {
			if a != "DescribeAutoScalingInstances" && a != "RecordLifecycleActionHeartbeat" {
				t.Errorf("%s after a hook it couldn't reach", a)
			}
		}
	})
	t.Run("a hook it can't complete is a failure the console shows", func(t *testing.T) {
		c := runInitWith(t, "lifecycle_hook: natkit-ready", true, func(c *initCase) {
			c.cl.completeErr = errors.New("CompleteLifecycleAction: HTTP 403 AccessDenied: not authorized")
		})
		c.wantFail(t, "CompleteLifecycleAction natkit-ready failed: natkit-init: CompleteLifecycleAction: HTTP 403 AccessDenied: not authorized")
	})
	t.Run("still Pending after 5 min", func(t *testing.T) {
		c := runInitWith(t, "lifecycle_hook: natkit-ready", true, func(c *initCase) { c.cl.asgStates = []string{"Pending"} })
		c.wantFail(t, "lifecycle_hook: still Pending in the Auto Scaling group after 5 min")
	})
}

// regression: a parameter put from a file (aws ssm put-parameter --value file://hash) ends with a newline; the shell's
// $(...) dropped it, and the shadow line must not carry it either
func TestInitConsolePasswordNewline(t *testing.T) {
	c := runInitWith(t, "console_password: /natkit/test/hash-nl", true, nil)
	c.wantReady(t, "ready, public eth0 private eth0, eip none, nat64 off, cwagent off")
	if got := c.h.file(runDir + "/getty.shadow"); got != "root:$6$salt$hash::0:99999:7:::\n" {
		t.Errorf("getty.shadow %q", got)
	}
}

func TestInitFailures(t *testing.T) {
	t.Run("a setup that fails takes neither the ENI nor the EIP, and leaves the hook alone", func(t *testing.T) {
		c := runInitWith(t, "eip_id: eipalloc-0123\neni_id: eni-0bbbbbbbbbbbbbbbb\nlifecycle_hook: natkit-ready\nconsole_password: /natkit/test/plain", true, nil)
		c.wantFail(t, "console_password: /natkit/test/plain is not a crypt hash (openssl passwd -6)")
		if !reflect.DeepEqual(c.cl.actions(), []string{"GetParameter"}) {
			t.Errorf("calls %v", c.cl.calls)
		}
		if got := c.h.file(runDir + "/getty.shadow"); got != lockedRow {
			t.Errorf("getty.shadow %q after a failed setup", got)
		}
	})
	t.Run("a failure after the ENI: the hook is completed all the same", func(t *testing.T) {
		c := runInitWith(t, "eip_id: eipalloc-0bad\neni_id: eni-0bbbbbbbbbbbbbbbb\nlifecycle_hook: natkit-ready", true, func(c *initCase) {
			c.h.mkfile("/sys/class/net/eth9/address", testStaticMAC+"\n")
			c.cl.associateErr = errors.New("AssociateAddress: HTTP 400 InvalidParameterValue: unexpected")
		})
		c.wantFail(t, "AssociateAddress eipalloc-0bad failed: natkit-init: AssociateAddress: HTTP 400 InvalidParameterValue: unexpected")
		if !strings.Contains(strings.Join(c.cl.calls, "\n"), "CompleteLifecycleAction") {
			t.Errorf("calls %v", c.cl.calls)
		}
	})
	t.Run("no instance metadata", func(t *testing.T) {
		c := runInitWith(t, "", true, func(c *initCase) { c.md.downFor = 30 })
		c.wantFail(t, "no instance metadata service")
		if c.h.slept != 30*time.Second {
			t.Errorf("slept %v", c.h.slept)
		}
	})
	t.Run("instance metadata a moment late", func(t *testing.T) {
		c := runInitWith(t, "", true, func(c *initCase) { c.md.downFor = 5 })
		c.wantReady(t, "ready, public eth0 private eth0, eip none, nat64 off, cwagent off")
	})
	t.Run("no region", func(t *testing.T) {
		c := runInitWith(t, "", true, func(c *initCase) { c.md.regionErr = errors.New("instance metadata placement/region: HTTP 404") })
		c.wantFail(t, "instance metadata placement/region: HTTP 404")
	})
	t.Run("user-data that can't be read is not an empty one", func(t *testing.T) {
		c := runInitWith(t, "", true, func(c *initCase) { c.md.userErr = errors.New("instance metadata user-data: HTTP 500") })
		c.wantFail(t, "instance metadata user-data: HTTP 500")
	})
	t.Run("bad user-data", func(t *testing.T) {
		c := runInitWith(t, "bogus: 1", true, nil)
		c.wantFail(t, "user-data: unknown key 'bogus'")
		if got := c.h.file(runDir + "/getty.shadow"); got != lockedRow {
			t.Errorf("getty.shadow %q", got)
		}
	})
	t.Run("an interface that doesn't exist", func(t *testing.T) {
		runInitWith(t, "interface: eth7", true, nil).wantFail(t, "interface: no interface eth7")
	})
	t.Run("no interface with the primary ENI's MAC", func(t *testing.T) {
		runInitWith(t, "", true, func(c *initCase) {
			c.md.values["mac"] = "02:00:00:00:00:01"
			c.md.values["network/interfaces/macs/02:00:00:00:00:01/interface-id"] = testENI
		}).
			wantFail(t, "no interface with the primary ENI's MAC 02:00:00:00:00:01")
	})
	t.Run("nftables refuses the ruleset", func(t *testing.T) {
		runInitWith(t, "", true, func(c *initCase) {
			c.h.answer = func(name string, _ []string, _ string) (string, error) {
				if name == "nft" {
					return "Error: syntax error\nmore", errors.New("nft: exit status 1")
				}
				return "", nil
			}
		}).wantFail(t, "nftables: Error: syntax error")
	})
	t.Run("cwagent without its parameter", func(t *testing.T) {
		runInitWith(t, "cwagent_enabled: true", true, nil).wantFail(t, "cwagent_enabled needs cwagent_cfg_param_name")
	})
	t.Run("cwagent config that isn't a JSON object", func(t *testing.T) {
		runInitWith(t, "cwagent_enabled: true\ncwagent_cfg_param_name: /natkit/test/notjson", true, nil).
			wantFail(t, "cwagent: /natkit/test/notjson is not a JSON object")
		runInitWith(t, "cwagent_enabled: true\ncwagent_cfg_param_name: /natkit/test/null", true, nil).
			wantFail(t, "cwagent: /natkit/test/null is not a JSON object")
	})
	t.Run("a console_password hash that would break the shadow line", func(t *testing.T) {
		for _, p := range []string{"/natkit/test/colon", "/natkit/test/two-lines"} {
			c := runInitWith(t, "console_password: "+p, true, nil)
			c.wantFail(t, "console_password: "+p+" is not a crypt hash (openssl passwd -6)")
			if got := c.h.file(runDir + "/getty.shadow"); got != lockedRow {
				t.Errorf("getty.shadow %q", got)
			}
		}
	})
	t.Run("an SSM parameter it can't read", func(t *testing.T) {
		c := runInitWith(t, "console_password: /natkit/test/missing", true, nil)
		c.wantFail(t, "ssm: can't read /natkit/test/missing for console_password: GetParameter: HTTP 400 ParameterNotFound")
		if n := len(c.cl.calls); n != 10 {
			t.Errorf("%d tries", n)
		}
	})
	t.Run("a panic still leaves a result line", func(t *testing.T) {
		c := runInitWith(t, "eip_id: eipalloc-0123", true, func(c *initCase) { c.cl.panicOn = "AssociateAddress eipalloc-0123 " + testENI })
		c.wantFail(t, "natkit-init stopped: boom in AssociateAddress")
	})
}

// a setting the kernel refuses is a failed boot, not a NAT that quietly doesn't forward: each write, refused in turn
// (a directory where the file is)
func TestInitSysctlRefused(t *testing.T) {
	for _, tc := range []struct {
		name, ud string
		testMode bool
		path     string
		want     string
	}{
		{"sysctl.conf", "", true, "/proc/sys/net/ipv4/ip_forward", "sysctl net.ipv4.ip_forward=1"},
		{"a kernel.* key, on the instance", "", false, "/proc/sys/kernel/dmesg_restrict", "sysctl kernel.dmesg_restrict=1"},
		{"user-data tuning", "tcp_keepalive_time: 600", true, "/proc/sys/net/ipv4/tcp_keepalive_time", "sysctl net.ipv4.tcp_keepalive_time=600"},
		{"the conntrack table", "nf_conntrack_max: 262144", true, "/proc/sys/net/netfilter/nf_conntrack_max", "sysctl net.netfilter.nf_conntrack_max=262144"},
		{"the conntrack buckets", "nf_conntrack_buckets: 4096", true, "/sys/module/nf_conntrack/parameters/hashsize", "nf_conntrack_buckets=4096"},
		{"modules_disabled", "", false, "/proc/sys/kernel/modules_disabled", "sysctl kernel.modules_disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := runInitWith(t, "eip_id: eipalloc-0123\n"+tc.ud, tc.testMode, func(c *initCase) {
				_ = os.Remove(c.h.path(tc.path))
				c.h.mkdir(tc.path)
			})
			c.wantFail(t, tc.want)
			if len(c.cl.calls) != 0 {
				t.Errorf("AWS calls after a refused setting: %v", c.cl.calls)
			}
		})
	}
	// per interface, after the ENI and the EIP: the ENI attached just now is one of them
	t.Run("an interface's rp_filter", func(t *testing.T) {
		c := runInitWith(t, "", true, func(c *initCase) {
			_ = os.Remove(c.h.path("/proc/sys/net/ipv4/conf/eth0/rp_filter"))
			c.h.mkdir("/proc/sys/net/ipv4/conf/eth0/rp_filter")
		})
		c.wantFail(t, "sysctl net.ipv4.conf.eth0.rp_filter=0")
	})
	t.Run("no sysctl.conf", func(t *testing.T) {
		c := runInitWith(t, "", true, func(c *initCase) { _ = os.Remove(c.h.path("/etc/natkit/sysctl.conf")) })
		c.wantFail(t, "sysctl: open ")
	})
}

// an API that refuses every try: the failure names the call, after 10 tries, and the ENI stays with its instance
func TestInitCallsRefused(t *testing.T) {
	denied := func(action string) error {
		return errors.New(action + ": HTTP 403 UnauthorizedOperation: not authorized")
	}
	t.Run("DescribeAutoScalingInstances", func(t *testing.T) {
		c := runInitWith(t, "eni_id: eni-0bbbbbbbbbbbbbbbb\nlifecycle_hook: natkit-ready", true, func(c *initCase) {
			c.cl.asgErr = denied("DescribeAutoScalingInstances")
		})
		c.wantFail(t, "DescribeAutoScalingInstances failed: natkit-init: DescribeAutoScalingInstances: HTTP 403 UnauthorizedOperation: not authorized")
		if a := c.cl.actions(); len(a) != 10 || slices.ContainsFunc(a, func(s string) bool { return s != "DescribeAutoScalingInstances" }) {
			t.Errorf("calls %v", c.cl.calls)
		}
		if c.h.slept != 27*time.Second {
			t.Errorf("slept %v", c.h.slept)
		}
	})
	t.Run("ModifyNetworkInterfaceAttribute", func(t *testing.T) {
		c := runInitWith(t, "eip_id: eipalloc-0123\neni_id: eni-0bbbbbbbbbbbbbbbb", true, func(c *initCase) {
			c.cl.sourceDestErr = denied("ModifyNetworkInterfaceAttribute")
		})
		c.wantFail(t, "ModifyNetworkInterfaceAttribute "+testENI+" failed: natkit-init: ModifyNetworkInterfaceAttribute: HTTP 403 UnauthorizedOperation: not authorized")
		if a := c.cl.actions(); len(a) != 10 || slices.ContainsFunc(a, func(s string) bool { return s != "ModifyNetworkInterfaceAttribute" }) {
			t.Errorf("calls %v", c.cl.calls)
		}
	})
}

func TestInitMetrics(t *testing.T) {
	t.Run("written once the instance is in service", func(t *testing.T) {
		c := runInitWith(t, "metrics: nat-a", true, func(c *initCase) { c.h.answer = ethtoolENA("153914") })
		c.wantReady(t, "ready, public eth0 private eth0, eip none, nat64 off, cwagent off")
		if got := c.h.file(runDir + "/metrics"); got != "nat-a\n" {
			t.Errorf("metrics %q", got)
		}
	})
	// regression: a replacement that failed its setup reported zeros under the NAT's name, and kept its alarm OK
	t.Run("not by an instance whose setup failed", func(t *testing.T) {
		c := runInitWith(t, "metrics: nat-a\nconsole_password: /natkit/test/plain", true, func(c *initCase) {
			c.h.answer = ethtoolENA("153914")
			c.h.mkfile(runDir+"/metrics", "stale\n")
		})
		c.wantFail(t, "console_password")
		if !strings.HasPrefix(c.h.file(runDir+"/metrics"), "<") {
			t.Error("a metrics file after a failed setup")
		}
	})
	t.Run("no counters: not an ENA interface", func(t *testing.T) {
		runInitWith(t, "metrics: nat-a", true, nil).wantFail(t, "metrics: no conntrack_allowance counters on eth0 (not an ENA interface?)")
	})
	t.Run("the allowance on the console", func(t *testing.T) {
		c := runInitWith(t, "nf_conntrack_max: 131072", true, func(c *initCase) { c.h.answer = ethtoolENA("51303") })
		if !strings.Contains(c.h.out.String(), "natkit-init: EC2 conntrack allowance 51303, nf_conntrack_max 131072") {
			t.Errorf("log:\n%s", c.h.out.String())
		}
	})
}

// regression: one flow per 8 kB left a t4g.nano's table at 55 000 flows against EC2's 154 000; the kernel's table
// filled first, where the allowance metrics don't see it
func TestInitConntrackDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, ud, available, max, buckets string
	}{
		{"nano: EC2's allowance", "", "153914", "153914\n", "38478\n"},
		{"t3.nano: memory", "", "25000", "55327\n", "13831\n"},
		{"no ENA counters: memory", "", "", "55327\n", "13831\n"},
		{"user-data wins", "nf_conntrack_max: 262144\nnf_conntrack_buckets: 65536", "153914", "262144\n", "65536\n"},
		{"only the table: a quarter as many buckets", "nf_conntrack_max: 100000", "153914", "100000\n", "25000\n"},
		{"only the buckets: the table by the allowance", "nf_conntrack_buckets: 4096", "153914", "153914\n", "4096\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := runInitWith(t, tc.ud, false, func(c *initCase) {
				if tc.available != "" {
					c.h.answer = ethtoolENA(tc.available)
				}
			})
			c.wantReady(t, "ready, public eth0 private eth0, eip none, nat64 off, cwagent off")
			if got := c.h.file("/proc/sys/net/netfilter/nf_conntrack_max"); got != tc.max {
				t.Errorf("nf_conntrack_max %q, want %q", got, tc.max)
			}
			if got := c.h.file("/sys/module/nf_conntrack/parameters/hashsize"); got != tc.buckets {
				t.Errorf("hashsize %q, want %q", got, tc.buckets)
			}
			if got := c.h.file("/proc/sys/kernel/modules_disabled"); got != "1\n" {
				t.Errorf("modules_disabled %q", got)
			}
		})
	}
	if conntrackDefault(442620, 0, false) != 55327 || conntrackDefault(1000, 5000, true) != 5000 || conntrackDefault(80000, 5000, true) != 10000 {
		t.Error("conntrackDefault")
	}
}

func TestInitNAT64(t *testing.T) {
	jool := func(c *initCase) {
		c.h.mkfile("/lib/modules/6.18.63-linuxkit/extra/jool.ko", "")
		c.h.mkfile("/lib/modules/6.18.63-linuxkit/extra/jool_common.ko", "")
		c.h.mkfile("/proc/modules", "nf_conntrack 1 0 - Live 0x0\n")
	}
	t.Run("on", func(t *testing.T) {
		c := runInitWith(t, "", true, jool)
		c.wantReady(t, "ready, public eth0 private eth0, eip none, nat64 on, cwagent off")
		if !reflect.DeepEqual(c.h.modules, []string{"jool_common.ko", "jool.ko"}) {
			t.Errorf("modules %v", c.h.modules)
		}
		want := []string{"nft -f -", "ethtool -S eth0", "jool instance flush", "jool instance add --netfilter --pool6 64:ff9b::/96"}
		if !reflect.DeepEqual(c.h.commands, want) {
			t.Errorf("commands %v", c.h.commands)
		}
	})
	t.Run("with client_cidrs: address-dependent filtering", func(t *testing.T) {
		c := runInitWith(t, "client_cidrs: [10.0.0.0/16]", true, jool)
		if got := c.h.commands[len(c.h.commands)-1]; got != "jool global update address-dependent-filtering true" {
			t.Errorf("last command %q", got)
		}
	})
	t.Run("modules already loaded", func(t *testing.T) {
		c := runInitWith(t, "", true, func(c *initCase) {
			jool(c)
			c.h.mkfile("/proc/modules", "jool 1 0 - Live 0x0\njool_common 1 1 jool, Live 0x0\n")
		})
		if len(c.h.modules) != 0 {
			t.Errorf("loaded %v again", c.h.modules)
		}
	})
	t.Run("Jool failing is skipped, as in fck-nat", func(t *testing.T) {
		c := runInitWith(t, "", true, func(c *initCase) {
			jool(c)
			c.h.answer = func(name string, _ []string, _ string) (string, error) {
				if name == "jool" {
					return "", errors.New("jool: exit status 1: nope")
				}
				return "", nil
			}
		})
		c.wantReady(t, "ready, public eth0 private eth0, eip none, nat64 off, cwagent off")
		if !strings.Contains(c.h.out.String(), "NAT64: Jool failed, skipped: jool: exit status 1: nope") {
			t.Errorf("log:\n%s", c.h.out.String())
		}
	})
	t.Run("off in user-data", func(t *testing.T) {
		c := runInitWith(t, "nat64: false", true, jool)
		if len(c.h.modules) != 0 || !strings.Contains(c.h.out.String(), "NAT64: off in user-data") {
			t.Errorf("modules %v, log:\n%s", c.h.modules, c.h.out.String())
		}
	})
}

func TestInitInterface(t *testing.T) {
	c := runInitWith(t, "interface: eth9", true, func(c *initCase) { c.h.mkfile("/sys/class/net/eth9/address", "02:00:00:00:00:09\n") })
	c.wantReady(t, "ready, public eth9 private eth9, eip none, nat64 off, cwagent off")
	c = runInitWith(t, "interface: eth9\neni_id: eni-0bbbbbbbbbbbbbbbb", true, func(c *initCase) {
		c.h.mkfile("/sys/class/net/eth9/address", testStaticMAC+"\n")
		*c.md.attached = true
	})
	c.wantReady(t, "ready, public eth0 private eth9, eip none, nat64 off, cwagent off") // interface: ignored with eni_id
}

func TestInitUserDataKept(t *testing.T) {
	c := runInitWith(t, "", true, nil)
	if got := c.h.file(runDir + "/user-data"); got != "{}" {
		t.Errorf("user-data %q", got)
	}
	if _, err := os.Stat(filepath.Join(c.h.root, runDir, "failed")); err == nil {
		t.Error("failed marker after a success")
	}
}

func TestRuleset(t *testing.T) {
	plain := ruleset("eth0", &userData{nat64: true})
	for _, want := range []string{
		"flush ruleset\n",
		`oifname "eth0" meta l4proto { tcp, udp } masquerade to :1024-61000 comment "NAT routing rule installed by natkit"`,
		`oifname "eth0" masquerade` + "\n",
		"type filter hook input priority filter; policy drop;",
		"type filter hook forward priority filter; policy accept;",
		"ct state invalid drop",
		"udp dport 68 accept", "udp dport 546 accept",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("no %q in\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "clients4") || strings.Contains(plain, "prerouting") {
		t.Errorf("client sets without client_cidrs:\n%s", plain)
	}
	u, err := parseUserData([]byte(`client_cidrs: [10.0.0.0/16, 10.0.1.0/24, "2600:1f18:aaaa:bb00::/56"]`))
	if err != nil {
		t.Fatal(err)
	}
	gated := ruleset("eth1", u)
	for _, want := range []string{
		"set clients4 { type ipv4_addr; flags interval; auto-merge; elements = { 10.0.0.0/16, 10.0.1.0/24 } }",
		"set clients6 { type ipv6_addr; flags interval; auto-merge; elements = { 2600:1f18:aaaa:bb00::/56 } }",
		"type filter hook forward priority filter; policy drop;",
		"ct state established,related accept\n\t\tip saddr @clients4 accept\n\t\tip6 saddr @clients6 accept",
		"type filter hook prerouting priority raw; policy accept;",
		"ip6 daddr 64:ff9b::/96 ip6 saddr != @clients6 drop",
		`oifname "eth1" masquerade`,
	} {
		if !strings.Contains(gated, want) {
			t.Errorf("no %q in\n%s", want, gated)
		}
	}
	for file, got := range map[string]string{"ruleset-plain.nft": plain, "ruleset-clients.nft": gated} {
		want, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s:\n%s\nwant\n%s", file, got, want)
		}
	}
	u4, _ := parseUserData([]byte(`client_cidrs: [192.168.0.0/24]`))
	if r := ruleset("eth0", u4); !strings.Contains(r, "set clients6 { type ipv6_addr; flags interval; auto-merge;  }") {
		t.Errorf("an IPv4-only list needs an empty IPv6 set:\n%s", r)
	}
}
