package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConsole(t *testing.T) {
	p := filepath.Join(t.TempDir(), "console")
	natkitInit := console{path: p}
	natkitInit.write("natkit: one")
	natkitInit.write("natkit: two") // natkit-init's last line is the result
	if b, _ := os.ReadFile(p); string(b) != "natkit: two\n" {
		t.Errorf("%q", b)
	}
	service := console{path: p, append: true}
	service.write("natkit-metrics: three")
	if b, _ := os.ReadFile(p); string(b) != "natkit: two\nnatkit-metrics: three\n" {
		t.Errorf("%q", b)
	}
	console{path: filepath.Join(p, "nope", "x")}.write("no console: nothing happens")
}

func TestCommands(t *testing.T) {
	out, err := runCommand("in\n", "sh", "-c", "cat; echo err >&2; exit 3")
	if err == nil || !strings.Contains(out, "in") || !strings.Contains(out, "err") || !strings.HasPrefix(err.Error(), "sh: exit status 3: in") {
		t.Errorf("out %q, err %v", out, err)
	}
	if out, err := runCommand("", "sh", "-c", "echo ok"); err != nil || out != "ok\n" {
		t.Errorf("out %q, err %v", out, err)
	}
	if _, err := runCommandFor(100*time.Millisecond, "sleep", "5"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: %v", err)
	}
	var w bytes.Buffer
	last := streamCommand(&w, 2, "sh", "-c", "echo 1; echo 2; echo 3 >&2; exit 4")
	if w.String() != "1\n2\n3\n" || !reflect.DeepEqual(last, []string{"3", "exit status 4"}) {
		t.Errorf("streamed %q, last %q", w.String(), last)
	}
	if last := streamCommand(&w, 8, "sh", "-c", "true"); len(last) != 0 {
		t.Errorf("last %q", last)
	}
	if lastLine("a\nb\n\n") != "b" || lastLine("") != "" || firstLine("x\ny") != "x" {
		t.Error("lastLine/firstLine")
	}
}

func TestHostFiles(t *testing.T) {
	h := &host{root: t.TempDir()}
	if err := os.MkdirAll(h.path("/proc/sys/net"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.write("/proc/sys/net/x", "1\n", 0o644); err != nil || !h.exists("/proc/sys/net/x") {
		t.Fatal(err)
	}
	if err := h.write("/proc/sys/net/x", "0\n", 0o644); err != nil {
		t.Fatal(err)
	}
	if s, _ := h.read("/proc/sys/net/x"); s != "0\n" {
		t.Errorf("%q", s)
	}
	if g := h.glob("/proc/sys/*/x"); !reflect.DeepEqual(g, []string{"/proc/sys/net/x"}) {
		t.Errorf("glob %v (paths relative to root)", g)
	}
	if err := h.write("/no/such/dir/x", "", 0o644); err == nil {
		t.Error("wrote under a directory that doesn't exist")
	}
}

func TestEnvConfig(t *testing.T) {
	t.Setenv("NATKIT_IMDS", "http://10.0.0.1:8080/latest")
	t.Setenv("NATKIT_EC2", "http://10.0.0.1:8080/")
	t.Setenv("NATKIT_CONSOLE", "/tmp/console")
	t.Setenv("NATKIT_METRICS_CYCLES", "3")
	c := envConfig()
	if c.imds != "http://10.0.0.1:8080" || !c.testing || c.ec2 != "http://10.0.0.1:8080/" || c.console != "/tmp/console" || c.metricsCycles != 3 {
		t.Errorf("%+v", c)
	}
	t.Setenv("NATKIT_IMDS", "")
	t.Setenv("NATKIT_CONSOLE", "")
	if c := envConfig(); c.testing || c.imds != "" || c.console != "/dev/console" {
		t.Errorf("on an instance: %+v", c)
	}
}
