package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMetadata: IMDS, as the mock in test/mock.py has it: one instance, its primary ENI, and the static ENI once
// attached.
type fakeMetadata struct {
	mu        sync.Mutex
	values    map[string]string // meta-data paths
	user      []byte
	userErr   error
	regionVal string
	regionErr error
	downFor   int // instance-id fails this many times first (IMDS not up yet)
	attached  *bool
}

const (
	testMAC, testENI             = "02:42:ac:11:00:99", "eni-0aaaaaaaaaaaaaaaa"
	testStaticMAC, testStaticENI = "02:42:ac:11:00:98", "eni-0bbbbbbbbbbbbbbbb"
	testIID                      = "i-0123456789abcdef0"
)

func newFakeMetadata(ud string) *fakeMetadata {
	attached := false
	return &fakeMetadata{
		values: map[string]string{
			"instance-id": testIID,
			"mac":         testMAC,
			"network/interfaces/macs/" + testMAC + "/interface-id":       testENI,
			"network/interfaces/macs/" + testStaticMAC + "/interface-id": testStaticENI,
		},
		user:      []byte(ud),
		regionVal: "us-east-1",
		attached:  &attached,
	}
}

func (m *fakeMetadata) get(_ context.Context, p string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p == "instance-id" && m.downFor > 0 {
		m.downFor--
		return "", errors.New("instance metadata instance-id: no answer in time")
	}
	if p == "network/interfaces/macs/" {
		l := testMAC + "/"
		if *m.attached {
			l += "\n" + testStaticMAC + "/"
		}
		return l, nil
	}
	if v, ok := m.values[p]; ok {
		return v, nil
	}
	return "", fmt.Errorf("instance metadata %s: HTTP 404", p)
}

func (m *fakeMetadata) userData(context.Context) ([]byte, error) { return m.user, m.userErr }
func (m *fakeMetadata) region(context.Context) (string, error)   { return m.regionVal, m.regionErr }

// fakeCloud: AWS, with every call recorded and each answer programmable
type fakeCloud struct {
	mu    sync.Mutex
	calls []string
	md    *fakeMetadata

	attachmentErrs []error // in turn, then nil
	attachmentID   string  // the old instance's attachment until detached
	detachErrs     []error
	attachErrs     []error
	asgStates      []string // in turn; the last one repeats
	asgErr         error
	heartbeatErr   error
	completeErr    error
	associateErr   error
	sourceDestErr  error
	params         map[string]string
	paramErr       error
	panicOn        string
	metricsErr     []error
	metrics        [][2]float64
}

func newFakeCloud(md *fakeMetadata) *fakeCloud {
	return &fakeCloud{
		md:           md,
		attachmentID: "eni-attach-0ccccccccccccccc",
		asgStates:    []string{"Pending", "Pending:Wait"},
		params: map[string]string{
			"/natkit/test/root-hash": "$6$salt$hash", "/natkit/test/plain": "hunter2",
			"/natkit/test/cw": `{"metrics": {"namespace": "natkit"}}`, "/natkit/test/notjson": "[1, 2]",
		},
	}
}

func (c *fakeCloud) record(call string) {
	c.mu.Lock()
	c.calls = append(c.calls, call)
	c.mu.Unlock()
	if call == c.panicOn {
		panic("boom in " + call)
	}
}

func pop(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}
	e := (*errs)[0]
	*errs = (*errs)[1:]
	return e
}

func (c *fakeCloud) sourceDestCheckOff(_ context.Context, eni string) error {
	c.record("ModifyNetworkInterfaceAttribute " + eni)
	return c.sourceDestErr
}

func (c *fakeCloud) attachment(_ context.Context, eni string) (string, error) {
	c.record("DescribeNetworkInterfaces " + eni)
	if err := pop(&c.attachmentErrs); err != nil {
		return "", err
	}
	return c.attachmentID, nil
}

func (c *fakeCloud) detach(_ context.Context, att string) error {
	c.record("DetachNetworkInterface " + att)
	if err := pop(&c.detachErrs); err != nil {
		return err
	}
	c.attachmentID = ""
	return nil
}

func (c *fakeCloud) attach(_ context.Context, instance, eni string) error {
	c.record("AttachNetworkInterface " + instance + " " + eni)
	if err := pop(&c.attachErrs); err != nil {
		return err
	}
	*c.md.attached = true
	return nil
}

func (c *fakeCloud) associateAddress(_ context.Context, alloc, eni string) error {
	c.record("AssociateAddress " + alloc + " " + eni)
	return c.associateErr
}

func (c *fakeCloud) asgInstance(_ context.Context, instance string) (string, string, error) {
	c.record("DescribeAutoScalingInstances " + instance)
	if c.asgErr != nil {
		return "", "", c.asgErr
	}
	s := c.asgStates[0]
	if len(c.asgStates) > 1 {
		c.asgStates = c.asgStates[1:]
	}
	if s == "none" {
		return "", "", nil
	}
	return "natkit-test", s, nil
}

func (c *fakeCloud) heartbeat(_ context.Context, group, hook, instance string) error {
	c.record("RecordLifecycleActionHeartbeat " + group + " " + hook + " " + instance)
	if hook != "natkit-ready" {
		return errors.New("RecordLifecycleActionHeartbeat: HTTP 400 ValidationError: No active Lifecycle Action found with instance ID " + instance)
	}
	return c.heartbeatErr
}

func (c *fakeCloud) complete(_ context.Context, group, hook, instance string) error {
	c.record("CompleteLifecycleAction " + group + " " + hook + " " + instance)
	return c.completeErr
}

func (c *fakeCloud) parameter(_ context.Context, name string) (string, error) {
	c.record("GetParameter " + name)
	if c.paramErr != nil {
		return "", c.paramErr
	}
	v, ok := c.params[name]
	if !ok {
		return "", errors.New("GetParameter: HTTP 400 ParameterNotFound: " + name)
	}
	return v, nil
}

func (c *fakeCloud) putMetrics(_ context.Context, name string, exceeded, available float64) error {
	c.record("PutMetricData " + name)
	c.metrics = append(c.metrics, [2]float64{exceeded, available})
	return pop(&c.metricsErr)
}

func (c *fakeCloud) actions() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var a []string
	for _, call := range c.calls {
		a = append(a, strings.Fields(call)[0])
	}
	return a
}

// fakeHost: a temporary root with what natkit-init reads (/sys/class/net, /etc/natkit/sysctl.conf, /proc/sys's
// directories, /proc/meminfo), commands answered by a function, and a clock that only counts.
type fakeHost struct {
	*host
	t        *testing.T
	out, err bytes.Buffer
	commands []string
	answer   func(name string, args []string, stdin string) (string, error)
	modules  []string
	slept    time.Duration
	linkedUp []string
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	root := t.TempDir()
	f := &fakeHost{t: t}
	f.host = &host{
		root: root,
		run: func(stdin, name string, args ...string) (string, error) {
			f.commands = append(f.commands, strings.TrimSpace(name+" "+strings.Join(args, " ")))
			if f.answer != nil {
				return f.answer(name, args, stdin)
			}
			return "", nil
		},
		runFor: func(_ time.Duration, name string, args ...string) (string, error) {
			return f.host.run("", name, args...)
		},
		stream: func(w io.Writer, keep int, name string, args ...string) []string {
			out, _ := f.host.run("", name, args...)
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) > keep {
				lines = lines[len(lines)-keep:]
			}
			return lines
		},
		loadModule: func(p string) error { f.modules = append(f.modules, filepath.Base(p)); return nil },
		linkUp:     func(n string) error { f.linkedUp = append(f.linkedUp, n); return nil },
		release:    func() string { return "6.18.63-linuxkit" },
		sleep:      func(d time.Duration) { f.slept += d },
	}
	f.host.stdout, f.host.stderr = &f.out, &f.err
	f.mkfile("/sys/class/net/eth0/address", testMAC+"\n")
	f.mkfile("/sys/class/net/lo/address", "00:00:00:00:00:00\n")
	f.mkfile("/proc/meminfo", "MemTotal:         442620 kB\nMemFree:          173760 kB\n")
	conf, err := os.ReadFile("sysctl.conf")
	if err != nil {
		t.Fatal(err)
	}
	f.mkfile("/etc/natkit/sysctl.conf", string(conf))
	for _, d := range []string{
		"/proc/sys/net/ipv4/conf/all", "/proc/sys/net/ipv4/conf/default", "/proc/sys/net/ipv4/conf/eth0",
		"/proc/sys/net/ipv6/conf/all", "/proc/sys/net/ipv6/conf/default", "/proc/sys/net/netfilter",
		"/proc/sys/kernel", "/sys/module/nf_conntrack/parameters", "/run",
	} {
		f.mkdir(d)
	}
	for _, k := range []string{"rp_filter", "send_redirects"} {
		for _, d := range []string{"all", "default", "eth0"} {
			f.mkfile("/proc/sys/net/ipv4/conf/"+d+"/"+k, "1\n")
		}
	}
	return f
}

func (f *fakeHost) mkdir(p string) {
	if err := os.MkdirAll(f.path(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeHost) mkfile(p, s string) {
	f.mkdir(filepath.Dir(p))
	if err := os.WriteFile(f.path(p), []byte(s), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeHost) file(p string) string {
	b, err := os.ReadFile(f.path(p))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// ethtoolENA: `ethtool -S` with the ENA driver's allowance counters
func ethtoolENA(available string) func(string, []string, string) (string, error) {
	return func(name string, args []string, _ string) (string, error) {
		if name == "ethtool" {
			return "NIC statistics:\n     tx_timeout: 0\n     conntrack_allowance_exceeded: 0\n     conntrack_allowance_available: " + available + "\n", nil
		}
		return "", nil
	}
}
