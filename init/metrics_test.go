package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go/encoding/cbor"
)

func TestParseEthtool(t *testing.T) {
	c := parseEthtool(`NIC statistics:
     tx_timeout: 0
     bw_in_allowance_exceeded: 412
     conntrack_allowance_exceeded: 7
     conntrack_allowance_available: 153909
     garbage
     conntrack_allowance_available: notanumber
`)
	if !c.haveExceeded || c.exceeded != 7 || !c.haveAvailable || c.available != 153909 {
		t.Errorf("%+v", c)
	}
	if c := parseEthtool("no counters here\n"); c.haveExceeded || c.haveAvailable {
		t.Errorf("%+v", c)
	}
}

func TestDropCounter(t *testing.T) {
	var d dropCounter
	steps := []struct {
		sum      uint64
		reported bool
		want     uint64
	}{
		{12, false, 0}, // the first reading is where the counting starts, even if its report fails
		{17, false, 5}, // that report failed too: not lost
		{22, true, 10}, // covers both
		{25, true, 3},
		{4, true, 0}, // fewer than at the last report: an interface went, count from here
		{9, true, 5},
	}
	for i, s := range steps {
		if got := d.since(s.sum); got != s.want {
			t.Errorf("step %d: %d dropped, want %d", i, got, s.want)
		}
		if s.reported {
			d.reported(s.sum)
		}
	}
}

// ethtoolLikeTest: test/ethtool's counters: exceeded grows with every call, by 3 on eth0 from 7 and by 2 on eth9
// from 5; the lower available is eth9's, not the first interface's
func ethtoolLikeTest() func(string, []string, string) (string, error) {
	calls := map[string]int{}
	return func(name string, args []string, _ string) (string, error) {
		if name != "ethtool" {
			return "", nil
		}
		var from, by, left int
		switch args[1] {
		case "eth0":
			from, by, left = 7, 3, 1300
		case "eth9":
			from, by, left = 5, 2, 1234
		default:
			return "", errors.New("ethtool: no stats")
		}
		n := calls[args[1]]
		calls[args[1]]++
		return fmt.Sprintf("     conntrack_allowance_exceeded: %d\n     conntrack_allowance_available: %d\n", from+by*n, left), nil
	}
}

func metricsHost(t *testing.T, name string) *fakeHost {
	h := newFakeHost(t)
	h.mkfile("/sys/class/net/eth9/address", testStaticMAC+"\n")
	h.answer = ethtoolLikeTest()
	if name != "" {
		h.mkfile(runDir+"/metrics", name+"\n")
	}
	return h
}

func TestReadCounters(t *testing.T) {
	h := metricsHost(t, "")
	c := readCounters(h.host)
	if !c.haveExceeded || c.exceeded != 12 || c.available != 1234 {
		t.Errorf("%+v", c)
	}
}

func runMetricsWith(t *testing.T, h *fakeHost, cycles int, tweak func(*fakeMetadata, *fakeCloud)) (int, *fakeCloud) {
	md := newFakeMetadata("")
	cl := newFakeCloud(md)
	if tweak != nil {
		tweak(md, cl)
	}
	p := &publisher{h: h.host, md: md, newCloud: func(string) cloud { return cl }}
	return metricsLoop(h.host, config{console: h.path("/console"), metricsCycles: cycles}, p), cl
}

func TestMetrics(t *testing.T) {
	t.Run("not enabled", func(t *testing.T) {
		h := metricsHost(t, "")
		if st, _ := runMetricsWith(t, h, 1, nil); st != 0 || !strings.Contains(h.out.String(), "natkit-metrics: not enabled") {
			t.Errorf("status %d, out %q", st, h.out.String())
		}
	})
	t.Run("a bad config", func(t *testing.T) {
		h := metricsHost(t, "")
		h.mkfile(runDir+"/metrics", "\n")
		if st, _ := runMetricsWith(t, h, 1, nil); st != 1 || h.file("/console") != "natkit-metrics: bad /run/natkit/metrics\n" {
			t.Errorf("status %d, console %q", st, h.file("/console"))
		}
	})
	t.Run("the drops since the last report, on every interface, under the name", func(t *testing.T) {
		h := metricsHost(t, "nat-a")
		st, cl := runMetricsWith(t, h, 2, nil)
		if st != 0 || !reflect.DeepEqual(cl.metrics, [][2]float64{{0, 1234}, {5, 1234}}) {
			t.Errorf("status %d, metrics %v", st, cl.metrics)
		}
		if got := h.file("/console"); got != "natkit-metrics: publishing to CloudWatch, namespace natkit, Name=nat-a\n" {
			t.Errorf("console %q (once, not every cycle)", got)
		}
		if cl.calls[0] != "PutMetricData nat-a" {
			t.Errorf("calls %v", cl.calls)
		}
	})
	// regression: a failed report's drops were lost to the next one
	t.Run("a report that fails is covered by the next", func(t *testing.T) {
		h := metricsHost(t, "nat-a")
		st, cl := runMetricsWith(t, h, 3, func(_ *fakeMetadata, cl *fakeCloud) {
			cl.metricsErr = []error{nil, errors.New("PutMetricData: HTTP 500 InternalFailure: try again")}
		})
		if st != 0 || !reflect.DeepEqual(cl.metrics, [][2]float64{{0, 1234}, {5, 1234}, {10, 1234}}) {
			t.Errorf("status %d, metrics %v", st, cl.metrics)
		}
		want := "natkit-metrics: publishing to CloudWatch, namespace natkit, Name=nat-a\n" +
			"natkit-metrics: can't publish: PutMetricData: HTTP 500 InternalFailure: try again\n" +
			"natkit-metrics: publishing to CloudWatch, namespace natkit, Name=nat-a\n"
		if got := h.file("/console"); got != want {
			t.Errorf("console %q", got)
		}
	})
	t.Run("no instance metadata yet: the counting still starts", func(t *testing.T) {
		h := metricsHost(t, "nat-a")
		st, cl := runMetricsWith(t, h, 2, func(md *fakeMetadata, _ *fakeCloud) {
			md.regionErr = errors.New("instance metadata placement/region: no answer in time")
		})
		if st != 1 || len(cl.metrics) != 0 || h.file("/console") != "natkit-metrics: can't publish: instance metadata placement/region: no answer in time\n" {
			t.Errorf("status %d, metrics %v, console %q", st, cl.metrics, h.file("/console"))
		}
	})
	t.Run("no counters", func(t *testing.T) {
		h := metricsHost(t, "nat-a")
		h.answer = nil
		if st, _ := runMetricsWith(t, h, 1, nil); st != 1 || h.file("/console") != "natkit-metrics: can't publish: no conntrack_allowance counters on any interface\n" {
			t.Errorf("status %d, console %q", st, h.file("/console"))
		}
	})
	t.Run("waits 50 s between reports", func(t *testing.T) {
		h := metricsHost(t, "nat-a")
		md := newFakeMetadata("")
		cl := newFakeCloud(md)
		p := &publisher{h: h.host, md: md, newCloud: func(string) cloud { return cl }}
		n := 0
		h.host.sleep = func(d time.Duration) {
			if d != metricsInterval {
				t.Errorf("slept %v", d)
			}
			if n++; n == 3 {
				panic("stop")
			}
		}
		func() {
			defer func() { recover() }()
			metricsLoop(h.host, config{console: h.path("/console")}, p)
		}()
		if len(cl.metrics) != 3 {
			t.Errorf("%d reports", len(cl.metrics))
		}
	})
}

// natkit-metrics end to end through the SDK, against the fake AWS: CloudWatch's RPC v2 CBOR on the wire
func TestRunMetricsSDK(t *testing.T) {
	s := newAWSServer(t)
	h := metricsHost(t, "nat-a")
	c := s.config()
	c.console, c.metricsCycles = filepath.Join(h.root, "console"), 2
	if st := runMetrics(h.host, c); st != 0 {
		t.Fatalf("status %d, console %q", st, h.file("/console"))
	}
	var got []float64
	for _, r := range s.reqs {
		m := r.cbor.(cbor.Map)
		for _, d := range m["MetricData"].(cbor.List) {
			if d := d.(cbor.Map); d["MetricName"] == cbor.String("conntrack_allowance_exceeded") {
				v, _ := cbor.AsFloat64(d["Value"])
				got = append(got, v)
			}
		}
	}
	if !reflect.DeepEqual(got, []float64{0, 5}) {
		t.Errorf("exceeded %v", got)
	}
}
