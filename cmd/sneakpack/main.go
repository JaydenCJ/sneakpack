// Command sneakpack is the offline directory-sync CLI. All behavior lives
// in internal/cli so it can be tested in-process; main only wires the real
// process streams and exit code.
package main

import (
	"os"

	"github.com/JaydenCJ/sneakpack/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
