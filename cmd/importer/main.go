// Command importer is the CLI and server entrypoint of data-importer.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is the build version; override with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

const usage = `usage: importer <command> [args]

commands:
  migrate   apply database migrations (planned)
  serve     run the gRPC server and scheduler (planned)
  run       run one import for a source (planned)
  mapping   validate and materialize mappings (planned)
  version   print the version
  help      print this help
`

// run dispatches a subcommand and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "migrate", "serve", "run", "mapping":
		fmt.Fprintf(stderr, "importer: %q is not implemented yet\n", args[0])
		return 1
	default:
		fmt.Fprintf(stderr, "importer: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
