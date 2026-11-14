package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestUserDataRefused(t *testing.T) {
	for _, tc := range []struct{ ud, want string }{
		// the cases test/init.sh had for the shell version, with the same reasons
		{"eip: eipalloc-0123", "user-data: unknown key 'eip'"},
		{"eip_id: eipalloc-0123; rm -rf /", "user-data: eip_id: bad value 'eipalloc-0123; rm -rf /'"},
		{"nf_conntrack_max: lots", "user-data: nf_conntrack_max: bad value 'lots'"},
		{"nat64: maybe", "user-data: nat64: bad value 'maybe'"},
		{`metrics: "a b"`, "user-data: metrics: bad value 'a b'"},
		{`lifecycle_hook: "a b"`, "user-data: lifecycle_hook: bad value 'a b'"},
		{"client_cidrs: 10.0.0.0/16", "user-data: client_cidrs: not a list of CIDRs"},
		{"client_cidrs: []", "user-data: client_cidrs: not a list of CIDRs"},
		{"client_cidrs: [10.0.0.0]", "user-data: client_cidrs: bad value '10.0.0.0'"},
		{`client_cidrs: ["10.0.0.0/16; flush ruleset"]`, "user-data: client_cidrs: bad value '10.0.0.0/16;'"},
		{"client_cidrs: [999.0.0.0/8]", "user-data: client_cidrs: bad value '999.0.0.0/8'"},
		{"client_cidrs: [010.0.0.0/8]", "user-data: client_cidrs: bad value '010.0.0.0/8'"},
		{"client_cidrs: [10.0.0.0/33]", "user-data: client_cidrs: bad value '10.0.0.0/33'"},
		{`client_cidrs: [""]`, "user-data: client_cidrs: not a list of CIDRs"},
		{`client_cidrs: ["2600:1f18::/129"]`, "user-data: client_cidrs: bad value '2600:1f18::/129'"},
		{`client_cidrs: ["2600:zz::/56"]`, "user-data: client_cidrs: bad value '2600:zz::/56'"},
		{"nf_conntrack_max: 0800000", "user-data: nf_conntrack_max: bad value '0800000'"},
		{"- a list", "user-data is not a YAML map"},
		// and more
		{"# only a comment", "user-data is not a YAML map"},
		{"just text", "user-data is not a YAML map"},
		{"a: [", "user-data is not a YAML map"},
		{"eip_id: a\neip_id: b", "user-data: eip_id: set twice"},
		{"eni_id: eni-XYZ", "user-data: eni_id: bad value 'eni-XYZ'"},
		{"interface: eth0123456789abcd", "user-data: interface: bad value 'eth0123456789abcd'"},
		{`interface: "eth0; reboot"`, "user-data: interface: bad value 'eth0; reboot'"},
		{`ip_local_port_range: "1024"`, "user-data: ip_local_port_range: bad value '1024'"},
		{"tcp_keepalive_time: -1", "user-data: tcp_keepalive_time: bad value '-1'"},
		{"tcp_max_syn_backlog: 1e3", "user-data: tcp_max_syn_backlog: bad value '1e3'"},
		{"cwagent_enabled: yes", "user-data: cwagent_enabled: bad value 'yes'"},
		{"cwagent_enabled: True", "user-data: cwagent_enabled: bad value 'True'"},
		{`nat64: "false"`, "user-data: nat64: bad value 'false'"},
		{"nat64: [1]", "user-data: nat64: bad value '[1]'"},
		{"console_password: /natkit/a b", "user-data: console_password: bad value '/natkit/a b'"},
		{"cwagent_cfg_param_name: /natkit/$x", "user-data: cwagent_cfg_param_name: bad value '/natkit/$x'"},
		{"eip_id: [eipalloc-0123]", "user-data: eip_id: bad value '[eipalloc-0123]'"},
		{"client_cidrs: [null]", "user-data: client_cidrs: bad value 'null'"},
		{"client_cidrs: [false]", "user-data: client_cidrs: bad value 'false'"},
		{"client_cidrs: {a: 1}", "user-data: client_cidrs: not a list of CIDRs"},
		{"client_cidrs: [10.0.0.0/8, 1.2.3.4/32 x]", "user-data: client_cidrs: bad value 'x'"},
		// regression: the shell's grep -x matched any line of a multi-line value
		{"eip_id: \"eipalloc-0123\\nrm -rf /\"", "user-data: eip_id: bad value 'eipalloc-0123\nrm -rf /'"},
		{"client_cidrs: [\"10.0.0.0/16\\n\"]", ""}, // whitespace splits: still a valid CIDR
	} {
		u, err := parseUserData([]byte(tc.ud))
		if tc.want == "" {
			if err != nil {
				t.Errorf("%q: refused: %v", tc.ud, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%q: accepted as %+v, want %q", tc.ud, u, tc.want)
		} else if err.Error() != tc.want {
			t.Errorf("%q: %q, want %q", tc.ud, err, tc.want)
		}
	}
}

func TestUserDataAccepted(t *testing.T) {
	u, err := parseUserData([]byte(`eip_id: eipalloc-0123
eni_id: eni-0bbbbbbbbbbbbbbbb
interface: eth1
client_cidrs: [10.0.0.0/16, 10.0.1.0/24, "2600:1f18:aaaa:bb00::/56"]
ip_local_port_range: "2048	60000"
nf_conntrack_max: 262144
nf_conntrack_buckets: 65536
nf_conntrack_tcp_timeout_established: 3600
tcp_keepalive_time: 600
tcp_max_syn_backlog: 4096
metrics: nat-a
cwagent_enabled: true
cwagent_cfg_param_name: /natkit/test/cw
console_password: /natkit/test/root-hash
nat64: false
lifecycle_hook: natkit-ready
`))
	if err != nil {
		t.Fatal(err)
	}
	want := &userData{
		eipID: "eipalloc-0123", eniID: "eni-0bbbbbbbbbbbbbbbb", iface: "eth1", portRange: "2048\t60000",
		conntrackMax: "262144", conntrackBuckets: "65536", tcpEstablished: "3600", keepaliveTime: "600", synBacklog: "4096",
		cwagentEnabled: true, cwagentParam: "/natkit/test/cw", consolePassword: "/natkit/test/root-hash",
		lifecycleHook: "natkit-ready", metrics: "nat-a", nat64: false, clients: true,
		clients4: []string{"10.0.0.0/16", "10.0.1.0/24"}, clients6: []string{"2600:1f18:aaaa:bb00::/56"},
	}
	if !reflect.DeepEqual(u, want) {
		t.Errorf("got %+v\nwant %+v", u, want)
	}

	for _, tc := range []struct {
		ud    string
		check func(*userData) bool
	}{
		{"", func(u *userData) bool { return u.nat64 && !u.clients && u.eniID == "" }},
		{"{}", func(u *userData) bool { return u.nat64 }},
		{"nat64: true", func(u *userData) bool { return u.nat64 }},
		{"nat64: False", func(u *userData) bool { return !u.nat64 }},
		{"nat64: null", func(u *userData) bool { return u.nat64 }},
		{"cwagent_enabled: false", func(u *userData) bool { return !u.cwagentEnabled }},
		{"metrics: false", func(u *userData) bool { return u.metrics == "" }}, // the shell's // "" read false as unset
		{"client_cidrs: null", func(u *userData) bool { return !u.clients }},
		{"client_cidrs: [192.168.0.0/24]", func(u *userData) bool { return u.clients && len(u.clients6) == 0 }},
		{"client_cidrs: ['::/0']", func(u *userData) bool { return len(u.clients6) == 1 }},
		{"nf_conntrack_max: 0", func(u *userData) bool { return u.conntrackMax == "0" }},
		{"x: &a eipalloc-0abc\neip_id: *a", nil}, // refused: x is unknown
		{"lifecycle_hook: a/b_c-1", func(u *userData) bool { return u.lifecycleHook == "a/b_c-1" }},
	} {
		u, err := parseUserData([]byte(tc.ud))
		if tc.check == nil {
			if err == nil {
				t.Errorf("%q: accepted", tc.ud)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.ud, err)
		} else if !tc.check(u) {
			t.Errorf("%q: %+v", tc.ud, u)
		}
	}
}

// FuzzUserData: no input crashes the parser, and what it accepts reaches nftables, URLs and files in shape.
func FuzzUserData(f *testing.F) {
	for _, s := range []string{
		"", "{}", "eip_id: eipalloc-0123", "client_cidrs: [10.0.0.0/16, '2600::/56']", "nat64: false",
		"client_cidrs: ['10.0.0.0/16; flush ruleset']", "- a", "a: [", "interface: eth0", "metrics: nat-a",
		"x: &a 1\nmetrics: *a", "client_cidrs: *b", "nf_conntrack_max: 0800000",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		u, err := parseUserData([]byte(s))
		if err != nil {
			return
		}
		for _, c := range u.clients4 {
			if !reV4.MatchString(c) {
				t.Fatalf("client %q accepted", c)
			}
		}
		for _, c := range u.clients6 {
			if !reV6.MatchString(c) {
				t.Fatalf("client %q accepted", c)
			}
		}
		for v, re := range map[string]interface{ MatchString(string) bool }{
			u.eipID: reEIP, u.eniID: reENI, u.iface: reIface, u.metrics: reName, u.lifecycleHook: reHook,
			u.cwagentParam: reParam, u.consolePassword: reParam,
		} {
			if v != "" && !re.MatchString(v) {
				t.Fatalf("%q accepted", v)
			}
		}
		if strings.ContainsAny(ruleset("eth0", u), "`$") {
			t.Fatalf("ruleset with shell characters: %q", s)
		}
	})
}
