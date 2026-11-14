package main

import (
	"fmt"
	"time"
)

// natkit-cwagent (service): the CloudWatch agent, only when natkit-init wrote its config (user-data cwagent_enabled).
// Not in the image: it would sit in RAM (~150+ MB unpacked) on every NAT instance. Pulled by digest through the
// system containerd (the host's ctr, bound in) into its own namespace, run in the host network with the config at
// /etc/cwagentconfig, the agent image's container config directory. Restarted if it exits.

// agentImage: Renovate keeps it current (renovate.json, "CloudWatch agent, pulled at boot")
const agentImage = "public.ecr.aws/cloudwatch-agent/cloudwatch-agent:1.300074.1b2005@sha256:f6ce19de7f57fe9c536eb01dbebfb4c997fa053fa891d02711858642dda8a3f4"

const (
	agentConf    = runDir + "/cwagent"
	ctrPath      = "/usr/bin/ctr"
	pullTimeout  = 5 * time.Minute // a pull whose connection died otherwise hangs for as long as TCP retransmits
	agentPause   = 10 * time.Second
	agentLastOut = 8 // the agent's last lines on the console when it exits
)

func runCwagent(h *host, c config) int { return cwagentLoop(h, c, 0) }

// cwagentLoop: runs the agent `runs` times (0: for as long as the instance runs)
func cwagentLoop(h *host, c config, runs int) int {
	if !h.exists(agentConf + "/cwagentconfig.json") {
		fmt.Fprintln(h.stdout, "natkit-cwagent: not enabled")
		return 0
	}
	con := console{path: c.console, append: true}
	say := func(s string) {
		con.write("natkit-cwagent: " + s)
		fmt.Fprintln(h.stderr, "natkit-cwagent: "+s)
	}
	ctr := func(args ...string) []string { return append([]string{"-n", "natkit"}, args...) }

	for {
		out, err := h.runFor(pullTimeout, ctrPath, ctr("image", "pull", agentImage)...)
		if err == nil {
			break
		}
		why := lastLine(out)
		if why == "" {
			why = "no answer in 5 min"
		}
		say("pull failed, retrying: " + why)
		h.sleep(agentPause)
	}
	say("pulled " + agentImage)
	for n := 1; runs == 0 || n <= runs; n++ {
		_, _ = h.run("", ctrPath, ctr("task", "kill", "-s", "KILL", "cwagent")...)
		_, _ = h.run("", ctrPath, ctr("container", "rm", "cwagent")...)
		// ctr run, unlike a LinuxKit service, gives the container no resolv.conf: the host's (dhcpcd's), read-only.
		// Its output goes to memlogd as natkit-cwagent's; the last lines also go on the console when it exits.
		last := h.stream(h.stderr, agentLastOut, ctrPath, ctr("run", "--rm", "--net-host",
			"--mount", "type=bind,src="+agentConf+",dst=/etc/cwagentconfig,options=rbind:ro",
			"--mount", "type=bind,src=/etc/resolv.conf,dst=/etc/resolv.conf,options=rbind:ro",
			agentImage, "cwagent")...)
		say("agent exited, restarting; its last output:")
		for _, l := range last {
			say("  " + l)
		}
		h.sleep(agentPause)
	}
	return 0
}
