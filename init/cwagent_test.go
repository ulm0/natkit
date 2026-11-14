package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCwagent(t *testing.T) {
	t.Run("not enabled", func(t *testing.T) {
		h := newFakeHost(t)
		if st := cwagentLoop(h.host, config{console: h.path("/console")}, 1); st != 0 || h.out.String() != "natkit-cwagent: not enabled\n" || len(h.commands) != 0 {
			t.Errorf("status %d, out %q, commands %v", st, h.out.String(), h.commands)
		}
	})
	t.Run("pulled after failures, run and restarted", func(t *testing.T) {
		h := newFakeHost(t)
		h.mkfile(agentConf+"/cwagentconfig.json", "{}\n")
		pulls := 0
		h.answer = func(name string, args []string, _ string) (string, error) {
			switch {
			case args[2] == "image":
				pulls++
				switch pulls {
				case 1:
					return "resolving...\nfailed to resolve reference: dial tcp: i/o timeout\n", errors.New("exit status 1")
				case 2:
					return "", context.DeadlineExceeded // no answer in 5 min
				}
			case args[2] == "run":
				return "agent starting\nline 2\nfatal: bad config\n", nil
			}
			return "", nil
		}
		if st := cwagentLoop(h.host, config{console: h.path("/console")}, 2); st != 0 {
			t.Fatalf("status %d", st)
		}
		want := "natkit-cwagent: pull failed, retrying: failed to resolve reference: dial tcp: i/o timeout\n" +
			"natkit-cwagent: pull failed, retrying: no answer in 5 min\n" +
			"natkit-cwagent: pulled " + agentImage + "\n"
		for i := 0; i < 2; i++ {
			want += "natkit-cwagent: agent exited, restarting; its last output:\n" +
				"natkit-cwagent:   agent starting\nnatkit-cwagent:   line 2\nnatkit-cwagent:   fatal: bad config\n"
		}
		if got := h.file("/console"); got != want {
			t.Errorf("console\n%s\nwant\n%s", got, want)
		}
		var runs []string
		for _, c := range h.commands {
			if strings.Contains(c, " run ") || strings.Contains(c, " kill ") || strings.Contains(c, " rm ") {
				runs = append(runs, strings.Join(strings.Fields(c)[3:5], " "))
			}
		}
		if !reflect.DeepEqual(runs, []string{"task kill", "container rm", "run --rm", "task kill", "container rm", "run --rm"}) {
			t.Errorf("commands %v", h.commands)
		}
		run := ""
		for _, c := range h.commands {
			if strings.Contains(c, " run --rm") {
				run = c
			}
		}
		for _, want := range []string{ctrPath + " -n natkit run --rm --net-host", "src=" + agentConf + ",dst=/etc/cwagentconfig,options=rbind:ro", "src=/etc/resolv.conf,dst=/etc/resolv.conf,options=rbind:ro", agentImage + " cwagent"} {
			if !strings.Contains(run, want) {
				t.Errorf("no %q in %q", want, run)
			}
		}
		if h.slept != 4*agentPause {
			t.Errorf("slept %v", h.slept)
		}
	})
	if !strings.Contains(agentImage, "@sha256:") {
		t.Error("the agent image isn't pinned by digest")
	}
	_ = time.Second
}
