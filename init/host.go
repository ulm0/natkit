package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// config: where natkit finds IMDS and the AWS endpoints, and whether it runs under test/init.sh. The NATKIT_*
// variables are test/init.sh's: on an instance none is set.
type config struct {
	imds          string // IMDS endpoint, without /latest; "" for the instance's
	ec2           string // endpoint overrides; "" for the region's
	autoscaling   string
	ssm           string
	cloudwatch    string
	console       string // the serial console
	testing       bool   // under test/init.sh (NATKIT_IMDS set): the kernel is the docker host's
	metricsCycles int    // natkit-metrics: that many cycles, without the wait (test/init.sh)
}

func envConfig() config {
	c := config{
		ec2:         os.Getenv("NATKIT_EC2"),
		autoscaling: os.Getenv("NATKIT_AUTOSCALING"),
		ssm:         os.Getenv("NATKIT_SSM"),
		cloudwatch:  os.Getenv("NATKIT_CLOUDWATCH"),
		console:     "/dev/console",
	}
	if v := os.Getenv("NATKIT_IMDS"); v != "" {
		c.imds = strings.TrimSuffix(strings.TrimSuffix(v, "/"), "/latest")
		c.testing = true
	}
	if v := os.Getenv("NATKIT_CONSOLE"); v != "" {
		c.console = v
	}
	c.metricsCycles, _ = strconv.Atoi(os.Getenv("NATKIT_METRICS_CYCLES"))
	return c
}

// host is what natkit does to the machine besides AWS: files (under root, "" on an instance and a temporary
// directory in the unit tests), commands, kernel modules, interfaces and time.
type host struct {
	root       string
	run        func(stdin, name string, args ...string) (string, error) // stdout and stderr together
	runFor     func(timeout time.Duration, name string, args ...string) (string, error)
	stream     func(w io.Writer, keep int, name string, args ...string) []string // output to w as it comes; its last lines
	loadModule func(path string) error
	linkUp     func(name string) error
	release    func() string // uname -r
	sleep      func(time.Duration)
	stdout     io.Writer
	stderr     io.Writer
}

func newHost() *host {
	return &host{
		run:        runCommand,
		runFor:     runCommandFor,
		stream:     streamCommand,
		loadModule: loadModule,
		linkUp:     linkUp,
		release:    kernelRelease,
		sleep:      time.Sleep,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
	}
}

func (h *host) path(p string) string { return filepath.Join(h.root, p) }

func (h *host) read(p string) (string, error) {
	b, err := os.ReadFile(h.path(p))
	return string(b), err
}

// write replaces a file's contents, as the shell's `echo >file` did: sysctls and module parameters included.
func (h *host) write(p, s string, perm os.FileMode) error {
	f, err := os.OpenFile(h.path(p), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = f.WriteString(s)
	return errors.Join(err, f.Close())
}

func (h *host) exists(p string) bool {
	_, err := os.Stat(h.path(p))
	return err == nil
}

func (h *host) glob(pattern string) []string {
	m, _ := filepath.Glob(h.path(pattern))
	for i := range m {
		m[i] = "/" + strings.TrimPrefix(strings.TrimPrefix(m[i], h.root), "/")
	}
	return m
}

func runCommand(stdin, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w: %s", name, err, firstLine(string(out)))
	}
	return string(out), nil
}

func runCommandFor(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}

// streamCommand: runs a command with its output going to w as it comes, and returns its last `keep` lines
func streamCommand(w io.Writer, keep int, name string, args ...string) []string {
	c := exec.Command(name, args...)
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	var last []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			fmt.Fprintln(w, sc.Text())
			if last = append(last, sc.Text()); len(last) > keep {
				last = last[1:]
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	err := c.Run()
	pw.Close()
	<-done // the reader is done with `last`
	if err != nil {
		last = append(last, err.Error())
	}
	if len(last) > keep {
		last = last[len(last)-keep:]
	}
	return last
}

// lastLine: a command's last non-empty line
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// firstLine: a command's or an error's first line, for a one-line console message.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// console: natkit's lines on the serial console (memlogd's log isn't reachable from outside the instance).
type console struct {
	path   string
	append bool // natkit-init replaces its line, so the last one is the result; the services add theirs
}

func (c console) write(line string) {
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if c.append {
		flag = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	f, err := os.OpenFile(c.path, flag, 0o644)
	if err != nil {
		return // no console: nothing else to tell
	}
	fmt.Fprintln(f, line)
	f.Close()
}
