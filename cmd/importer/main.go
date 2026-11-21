// Command importer is the CLI and server entrypoint of data-importer.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/willis-dev-md/data-importer/internal/mapping"
	"github.com/willis-dev-md/data-importer/internal/store"
)

// version is the build version; override with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

const usage = `usage: importer <command> [args]

commands:
  migrate   apply database migrations: migrate up|down|version
  serve     run the gRPC server and scheduler (planned)
  run       run one import: run --source <id> [--dry-run]
  sources   sources apply [--dir <dir>] [--schema canonical-schema.yaml]
  mapping   mapping validate [--schema canonical-schema.yaml] <file>
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
	case "migrate":
		return runMigrate(args[1:], getenv("DATABASE_URL"), stdout, stderr)
	case "mapping":
		return runMapping(args[1:], stdout, stderr)
	case "sources":
		return runSources(args[1:], stdout, stderr)
	case "run":
		return runImport(args[1:], stdout, stderr)
	case "serve":
		fmt.Fprintf(stderr, "importer: %q is not implemented yet\n", args[0])
		return 1
	default:
		fmt.Fprintf(stderr, "importer: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// runMigrate implements `importer migrate up|down|version`. down rolls back one migration.
func runMigrate(args []string, databaseURL string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: importer migrate up|down|version")
		return 2
	}
	dir := store.Direction(args[0])
	switch dir {
	case store.Up, store.Down, store.Version:
	default:
		fmt.Fprintf(stderr, "importer migrate: unknown direction %q (want up, down or version)\n", args[0])
		return 2
	}
	if databaseURL == "" {
		fmt.Fprintln(stderr, "importer migrate: DATABASE_URL is not set")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	v, err := store.Migrate(ctx, databaseURL, dir)
	if err != nil {
		fmt.Fprintf(stderr, "importer migrate %s: %v\n", dir, err)
		return 1
	}
	fmt.Fprintf(stdout, "version %d\n", v)
	return 0
}

// runMapping implements `importer mapping validate [--schema file] <file>`.
// Exit codes: 0 no errors (warnings allowed), 1 validation errors, 2 usage or unreadable file.
func runMapping(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: importer mapping validate [--schema canonical-schema.yaml] <file>"
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("mapping validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	schema := fs.String("schema", "canonical-schema.yaml", "canonical schema file")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	file := fs.Arg(0)
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "importer mapping validate: %v\n", err)
		return 2
	}
	canonical, err := mapping.LoadCanonical(*schema)
	if err != nil {
		fmt.Fprintf(stderr, "importer mapping validate: %v\n", err)
		return 2
	}
	m, err := mapping.Parse(data)
	if err != nil {
		fmt.Fprintf(stdout, "%s: PARSE_ERROR: %v\n", file, err)
		return 1
	}
	issues := mapping.Validate(m, canonical)
	for _, i := range issues {
		fmt.Fprintln(stdout, i)
	}
	if mapping.HasErrors(issues) {
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
