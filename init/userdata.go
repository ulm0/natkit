package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// userData: the EC2 user-data, YAML with fck-nat's /etc/fck-nat.conf keys, all optional:
//
//	eip_id: eipalloc-0123                 # associate this EIP with the primary ENI (after eni_id's ENI is attached)
//	eni_id: eni-0123                      # static ENI (the private route tables' target): attach as device 1 (taking
//	                                      #   it over from an instance that holds it), turn off the primary ENI's
//	                                      #   source/dest check
//	nat64: false                          # no NAT64 (default: on)
//	interface: eth0                       # public interface, if not the primary ENI's (ignored with eni_id)
//	client_cidrs: [10.0.0.0/16]           # only these sources may open connections through the NAT (default: any)
//	ip_local_port_range: "1024 61000"     # sysctl tuning, as in fck-nat
//	nf_conntrack_max: 262144
//	nf_conntrack_buckets: 65536
//	nf_conntrack_tcp_timeout_established: 86400
//	tcp_keepalive_time: 600
//	tcp_max_syn_backlog: 4096
//	metrics: nat-a                        # EC2's conntrack allowance counters to CloudWatch (natkit-metrics),
//	                                      #   namespace natkit, dimension Name=nat-a; no agent
//	cwagent_enabled: true                 # CloudWatch agent (natkit-cwagent) with the JSON config in this
//	cwagent_cfg_param_name: /natkit/cw    #   SSM parameter
//	lifecycle_hook: natkit-ready          # the Auto Scaling group's launch lifecycle hook, completed once the
//	                                      #   instance has the ENI: until then the group keeps the old instance
//	console_password: /natkit/root-hash   # SSM parameter: root's crypt hash for the serial console getty
type userData struct {
	eipID, eniID, iface, portRange                        string
	conntrackMax, conntrackBuckets, tcpEstablished        string
	keepaliveTime, synBacklog                             string
	cwagentEnabled                                        bool
	cwagentParam, consolePassword, lifecycleHook, metrics string
	nat64                                                 bool
	clients                                               bool // client_cidrs is set
	clients4, clients6                                    []string
}

var userDataKeys = []string{
	"eip_id", "eni_id", "interface", "client_cidrs", "ip_local_port_range", "nf_conntrack_max", "nf_conntrack_buckets",
	"nf_conntrack_tcp_timeout_established", "tcp_keepalive_time", "tcp_max_syn_backlog", "cwagent_enabled",
	"cwagent_cfg_param_name", "console_password", "nat64", "lifecycle_hook", "metrics",
}

var (
	// every value in its expected shape: some reach nft's ruleset, a URL or a file
	reEIP       = regexp.MustCompile(`^eipalloc-[0-9a-f]+$`)
	reENI       = regexp.MustCompile(`^eni-[0-9a-f]+$`)
	reIface     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)
	rePortRange = regexp.MustCompile(`^[0-9]+[ \t]+[0-9]+$`)
	reNumber    = regexp.MustCompile(`^(0|[1-9][0-9]*)$`) // no leading zeros: 0800000 isn't 800000 anywhere
	reBool      = regexp.MustCompile(`^(true|false)$`)
	reName      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)
	reParam     = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	reHook      = regexp.MustCompile(`^[A-Za-z0-9_/-]{1,255}$`)
	// IPv4 octets without leading zeros: nft would read 010.0.0.0/8 as octal, 8.0.0.0/8. nft checks the IPv6
	// addresses itself.
	octet = `(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])`
	reV4  = regexp.MustCompile(`^(` + octet + `\.){3}` + octet + `/(3[0-2]|[12]?[0-9])$`)
	reV6  = regexp.MustCompile(`^[0-9A-Fa-f:]*:[0-9A-Fa-f:]*/(12[0-8]|1[01][0-9]|[1-9]?[0-9])$`)
)

// parseUserData: the user-data's settings, or why they're refused. Every key must be known (a typo would
// otherwise silently do nothing) and every value in its expected shape.
func parseUserData(b []byte) (*userData, error) {
	m := map[string]*yaml.Node{}
	if len(b) > 0 { // none at all is a plain NAT; anything else must be a map, even a comment alone
		dec := yaml.NewDecoder(bytes.NewReader(b))
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil || len(doc.Content) != 1 || resolve(doc.Content[0]).Kind != yaml.MappingNode {
			return nil, errors.New("user-data is not a YAML map")
		}
		// a second document would be settings natkit never reads: a NAT set up without them
		var more yaml.Node
		if err := dec.Decode(&more); err != io.EOF && !(err == nil && (len(more.Content) == 0 || isNull(more.Content[0]))) {
			return nil, errors.New("user-data: more than one YAML document")
		}
		root := resolve(doc.Content[0])
		for i := 0; i+1 < len(root.Content); i += 2 {
			k := root.Content[i].Value
			if !slices.Contains(userDataKeys, k) {
				return nil, fmt.Errorf("user-data: unknown key '%s'", k)
			}
			if _, ok := m[k]; ok {
				return nil, fmt.Errorf("user-data: %s: set twice", k)
			}
			m[k] = root.Content[i+1]
		}
	}

	u := &userData{nat64: true}
	var err error
	check := func(k string, re *regexp.Regexp) string {
		v := value(m[k])
		if err == nil && v != "" && !re.MatchString(v) {
			err = fmt.Errorf("user-data: %s: bad value '%s'", k, v)
		}
		return v
	}
	u.eipID = check("eip_id", reEIP)
	u.eniID = check("eni_id", reENI)
	u.iface = check("interface", reIface)
	u.portRange = check("ip_local_port_range", rePortRange)
	u.conntrackMax = check("nf_conntrack_max", reNumber)
	u.conntrackBuckets = check("nf_conntrack_buckets", reNumber)
	u.tcpEstablished = check("nf_conntrack_tcp_timeout_established", reNumber)
	u.keepaliveTime = check("tcp_keepalive_time", reNumber)
	u.synBacklog = check("tcp_max_syn_backlog", reNumber)
	u.cwagentEnabled = check("cwagent_enabled", reBool) == "true"
	u.metrics = check("metrics", reName)
	if err != nil {
		return nil, err
	}
	// value() reads false as unset: nat64 is the one key where false means something
	if n := m["nat64"]; n != nil && !isNull(n) {
		n = resolve(n)
		if n.Kind != yaml.ScalarNode || (n.Value != "true" && n.Value != "false") {
			return nil, fmt.Errorf("user-data: nat64: bad value '%s'", text(n))
		}
		u.nat64 = n.Value == "true"
	}
	u.cwagentParam = check("cwagent_cfg_param_name", reParam)
	u.consolePassword = check("console_password", reParam)
	u.lifecycleHook = check("lifecycle_hook", reHook)
	if err != nil {
		return nil, err
	}

	// client_cidrs: a non-empty list of CIDRs, split by family for nftables
	if n := m["client_cidrs"]; n != nil && !isNull(n) {
		n = resolve(n)
		if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
			return nil, errors.New("user-data: client_cidrs: not a list of CIDRs")
		}
		u.clients = true
		for _, e := range n.Content {
			// split on whitespace as the shell did: "10.0.0.0/16; flush ruleset" is refused at its first word
			for _, c := range strings.Fields(element(e)) {
				switch {
				case reV4.MatchString(c):
					u.clients4 = append(u.clients4, c)
				case reV6.MatchString(c):
					u.clients6 = append(u.clients6, c)
				default:
					return nil, fmt.Errorf("user-data: client_cidrs: bad value '%s'", c)
				}
			}
		}
		// blank entries are no words at all: a list of them would silently forward nothing
		if len(u.clients4)+len(u.clients6) == 0 {
			return nil, errors.New("user-data: client_cidrs: not a list of CIDRs")
		}
	}
	return u, nil
}

func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func isNull(n *yaml.Node) bool {
	n = resolve(n)
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// value: a key's value as text, the way the shell's $(yq '.[k] // "" | tostring') had it: unset, null and false
// are "", a scalar its text as written (0800000 stays 0800000) without trailing newlines (a | or > block's), anything
// else its YAML.
func value(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	n = resolve(n)
	if n.Kind == yaml.ScalarNode {
		if n.Tag == "!!null" || (n.Tag == "!!bool" && strings.EqualFold(n.Value, "false")) {
			return ""
		}
		return strings.TrimRight(n.Value, "\n")
	}
	return text(n)
}

// element: a list entry as text, as written (yq's tostring): an empty entry is "", skipped like a blank one
func element(n *yaml.Node) string {
	n = resolve(n)
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	return text(n)
}

func text(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	b, _ := yaml.Marshal(n)
	return strings.TrimSpace(string(b))
}
