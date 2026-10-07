// Command money-checks runs money invariants against an application's database and
// reports every row that breaks one.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is set at build time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

// Exit codes.
const (
	exitOK    = 0
	exitError = 2
)

const usage = `usage:
  money-checks version`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, version)
		return exitOK
	}
	fmt.Fprintln(stderr, usage)
	return exitError
}
