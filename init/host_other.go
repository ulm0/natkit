//go:build !linux

package main

import "errors"

// natkit only runs on Linux; these let its unit tests build elsewhere (they use fakes).
var errNotLinux = errors.New("not on Linux")

func loadModule(string) error { return errNotLinux }
func linkUp(string) error     { return errNotLinux }
func kernelRelease() string   { return "" }
