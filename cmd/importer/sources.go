package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/mapping"
	"github.com/willis-dev-md/data-importer/internal/store"
)

// getenv is the process environment; tests may replace it.
var getenv = os.Getenv

const (
	exitOK       = 0
	exitFailed   = 1
	exitUsage    = 2
	exitRunning  = 3
	defaultAppBy = "cli"
)

// openStore connects to DATABASE_URL. The caller must call the returned close func.
func openStore(ctx context.Context, cmd string, stderr io.Writer) (*store.Store, func(), int) {
	url := getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintf(stderr, "importer %s: DATABASE_URL is not set\n", cmd)
		return nil, nil, exitUsage
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		fmt.Fprintf(stderr, "importer %s: connect to database: %v\n", cmd, err)
		return nil, nil, exitFailed
	}
	return store.New(pool), pool.Close, exitOK
}

// runSources implements `importer sources apply [--dir <dir>] [--schema <file>]`.
// Exit codes: 0 ok, 1 invalid files or database error (nothing written for invalid files), 2 usage.
func runSources(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: importer sources apply [--dir <dir>] [--schema canonical-schema.yaml]   (--dir defaults to $SOURCES_DIR)"
	if len(args) == 0 || args[0] != "apply" {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	fs := flag.NewFlagSet("sources apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "directory of *.source.yaml and *.mapping.yaml (default $SOURCES_DIR)")
	schema := fs.String("schema", "canonical-schema.yaml", "canonical schema file")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *dir == "" {
		*dir = getenv("SOURCES_DIR")
	}
	if *dir == "" {
		fmt.Fprintln(stderr, "importer sources apply: no directory: pass --dir or set SOURCES_DIR")
		return exitUsage
	}
	if getenv("DATABASE_URL") == "" {
		fmt.Fprintln(stderr, "importer sources apply: DATABASE_URL is not set")
		return exitUsage
	}
	if st, err := os.Stat(*dir); err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "importer sources apply: %q is not a readable directory\n", *dir)
		return exitUsage
	}
	canonical, err := mapping.LoadCanonical(*schema)
	if err != nil {
		fmt.Fprintf(stderr, "importer sources apply: %v\n", err)
		return exitUsage
	}

	files, err := config.LoadDir(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "importer sources apply: %v\nnothing was written\n", err)
		return exitFailed
	}
	items, bad := make([]store.ApplyItem, 0, len(files)), false
	for _, f := range files {
		item, ok := prepareItem(f, canonical, stdout)
		bad = bad || !ok
		items = append(items, item)
	}
	if bad {
		fmt.Fprintln(stderr, "importer sources apply: invalid files; nothing was written")
		return exitFailed
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, closeDB, code := openStore(ctx, "sources apply", stderr)
	if code != exitOK {
		return code
	}
	defer closeDB()
	results, err := st.Apply(ctx, items)
	if err != nil {
		fmt.Fprintf(stderr, "importer sources apply: %v\nnothing was written\n", err)
		return exitFailed
	}
	for _, r := range results {
		src := "unchanged"
		switch {
		case r.SourceCreated:
			src = "created"
		case r.SourceUpdated:
			src = "updated"
		}
		m := "none"
		switch {
		case r.MappingCreated:
			m = fmt.Sprintf("v%d created", r.MappingVersion)
		case r.MappingVersion > 0:
			m = fmt.Sprintf("v%d unchanged", r.MappingVersion)
		}
		fmt.Fprintf(stdout, "%s: source %s, mapping %s\n", r.SourceID, src, m)
	}
	return exitOK
}

// prepareItem validates one source file (already validated by LoadDir) and its
// mapping, printing issues as `<file>: <issue>`. ok is false if anything is an error.
func prepareItem(f config.SourceFile, canonical *mapping.Canonical, stdout io.Writer) (store.ApplyItem, bool) {
	cfg, err := importer.ConfigJSON(f.Source)
	if err != nil {
		fmt.Fprintf(stdout, "%s: %v\n", f.SourcePath, err)
		return store.ApplyItem{}, false
	}
	item := store.ApplyItem{
		Source:    store.Source{ID: f.Source.ID, Kind: f.Source.Kind, Config: cfg},
		CreatedBy: defaultAppBy,
	}
	if f.MappingPath == "" {
		return item, true
	}
	ok := true
	m, err := mapping.Parse(f.MappingData)
	if err != nil {
		fmt.Fprintf(stdout, "%s: PARSE_ERROR: %v\n", f.MappingPath, err)
		return item, false
	}
	if m.Source != f.Source.ID {
		fmt.Fprintf(stdout, "%s: SOURCE_MISMATCH: mapping says source %q but %s is source %q\n", f.MappingPath, m.Source, f.SourcePath, f.Source.ID)
		ok = false
	}
	issues := mapping.Validate(m, canonical)
	for _, i := range issues {
		fmt.Fprintf(stdout, "%s: %s\n", f.MappingPath, i)
	}
	if mapping.HasErrors(issues) {
		ok = false
	}
	spec, err := specJSON(f.MappingData)
	if err != nil {
		fmt.Fprintf(stdout, "%s: %v\n", f.MappingPath, err)
		return item, false
	}
	item.Spec = spec
	return item, ok
}

// specJSON converts the mapping YAML to JSON as written (not via the typed
// struct), so what is stored is what the author wrote.
func specJSON(data []byte) (json.RawMessage, error) {
	var generic any
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&generic); err != nil {
		return nil, fmt.Errorf("mapping: %w", err)
	}
	b, err := json.Marshal(jsonable(generic))
	if err != nil {
		return nil, fmt.Errorf("mapping: %w", err)
	}
	return b, nil
}

// jsonable turns YAML maps with non-string keys into string-keyed maps.
func jsonable(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = jsonable(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = jsonable(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = jsonable(e)
		}
		return x
	}
	return v
}
