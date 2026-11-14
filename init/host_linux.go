package main

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// loadModule: insmod, without the modules' dependencies (Jool's two are loaded in order).
func loadModule(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.FinitModule(int(f.Fd()), "", 0); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	return nil
}

// linkUp: ip link set dev <name> up.
func linkUp(name string) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

func kernelRelease() string {
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return ""
	}
	return strings.TrimRight(string(u.Release[:]), "\x00")
}
