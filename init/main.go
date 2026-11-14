// natkit: the programs of natkit's image in one binary. natkit.yml runs it under three names (symlinks):
//
//	natkit-init     onboot, every boot: the NAT's setup from the EC2 user-data (natkit-init.go)
//	natkit-metrics  service: EC2's connection-tracking allowance counters to CloudWatch (metrics.go)
//	natkit-cwagent  service: the CloudWatch agent, pulled at boot (cwagent.go)
//
// `natkit init`, `natkit metrics` and `natkit cwagent` do the same.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// version is set at build time (-ldflags -X main.version=...), from git describe.
var version = "dev"

func main() {
	name, args := command(os.Args)
	if len(args) > 0 && (args[0] == "--version" || args[0] == "version") {
		fmt.Println(version)
		return
	}
	switch name {
	case "natkit-init":
		os.Exit(runInit(newHost(), envConfig()))
	case "natkit-metrics":
		os.Exit(runMetrics(newHost(), envConfig()))
	case "natkit-cwagent":
		os.Exit(runCwagent(newHost(), envConfig()))
	case "natkit-version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "natkit %s: run as natkit-init, natkit-metrics or natkit-cwagent, or `natkit %s`\n",
			version, strings.Join([]string{"init", "metrics", "cwagent"}, "|"))
		os.Exit(2)
	}
}

// command: which program, by the name it was run as (natkit-init) or its first argument (natkit init)
func command(argv []string) (string, []string) {
	name, args := filepath.Base(argv[0]), argv[1:]
	if name == "natkit" && len(args) > 0 {
		name, args = "natkit-"+args[0], args[1:]
	}
	return name, args
}
