package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/source"
	"github.com/willis-dev-md/data-importer/internal/store"
)

// defaultFileStoreDir is where fetched files are kept when FILE_STORE_DIR is not set
// (relative to the working directory).
const defaultFileStoreDir = "data/files"

// openFileStore opens the directory file store named by FILE_STORE_DIR (default
// defaultFileStoreDir). A directory that cannot be created or written is a usage
// error, reported before any database work.
func openFileStore(cmd string, stderr io.Writer) (store.FileStore, int) {
	dir := getenv("FILE_STORE_DIR")
	if dir == "" {
		dir = defaultFileStoreDir
	}
	fs, err := store.NewDirFileStore(dir)
	if err != nil {
		fmt.Fprintf(stderr, "importer %s: FILE_STORE_DIR: %v\n", cmd, err)
		return nil, exitUsage
	}
	// The default is relative to the working directory: say where files really go,
	// so running from another directory is not a silent switch to an empty store.
	fmt.Fprintf(stderr, "importer %s: file store: %s\n", cmd, fs.Dir())
	return fs, exitOK
}

// runReparse implements `importer reparse --source <id> [--run <id>]`: it parses the
// file stored for the source's latest run (or for run <id>) again with the current
// source config, with no network access, as a new run with trigger manual. It goes
// through importer.Run, so the lock, duplicate-key rules and counts are those of `run`.
// Exit codes (as for run): 0 ok, 1 failed, aborted or nothing to reparse, 2 usage
// (missing argument, bad --run, bad FILE_STORE_DIR or env), 3 already running.
func runReparse(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: importer reparse --source <id> [--run <id>]"
	fs := flag.NewFlagSet("reparse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("source", "", "source id")
	runID := fs.Int64("run", 0, "run whose stored file to parse again (default: the source's latest run with a file)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	runSet := false
	fs.Visit(func(f *flag.Flag) { runSet = runSet || f.Name == "run" })
	if *id == "" || fs.NArg() != 0 || (runSet && *runID <= 0) {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	files, code := openFileStore("reparse", stderr)
	if code != exitOK {
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, closeDB, code := openStore(ctx, "reparse", stderr)
	if code != exitOK {
		return code
	}
	defer closeDB()

	ids, err := st.SweepAbandonedRuns(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "importer reparse: %v\n", err)
		return exitFailed
	}
	if len(ids) > 0 {
		fmt.Fprintf(stderr, "importer reparse: marked %d abandoned run(s) failed: %v\n", len(ids), ids)
	}

	// No auth client and no fetch options: a reparse never opens a connection.
	start := time.Now()
	res, err := importer.Run(ctx, importer.Deps{
		Store: st,
		Adapters: map[string]source.Adapter{
			config.KindCSV: source.NewCSV(source.FetchOptions{}), config.KindXLSX: source.NewXLSX(source.FetchOptions{}),
		},
		Files: files,
	}, *id, importer.Options{Trigger: "manual", Reparse: &importer.ReparseOptions{RunID: *runID}})
	dur := time.Since(start).Round(time.Millisecond)
	if errors.Is(err, importer.ErrAlreadyRunning) {
		fmt.Fprintf(stderr, "importer reparse: %v\n", err)
		return exitRunning
	}
	if res.Status == "" { // never started a run
		fmt.Fprintf(stderr, "importer reparse: %v\n", err)
		return exitFailed
	}
	fmt.Fprintln(stdout, summary(*id, res, dur))
	if err != nil {
		fmt.Fprintf(stderr, "importer reparse: %v\n", err)
		return exitFailed
	}
	return exitOK
}

// runImport implements `importer run --source <id> [--dry-run]`.
// Exit codes: 0 ok, 1 failed or aborted, 2 usage (missing arg or env), 3 already running.
func runImport(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: importer run --source <id> [--dry-run]"
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("source", "", "source id")
	dry := fs.Bool("dry-run", false, "parse and count only; write nothing")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *id == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}

	var files store.FileStore // a dry run writes nothing, so it keeps no file
	if !*dry {
		var code int
		if files, code = openFileStore("run", stderr); code != exitOK {
			return code
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, closeDB, code := openStore(ctx, "run", stderr)
	if code != exitOK {
		return code
	}
	defer closeDB()

	src, err := st.GetSource(ctx, *id)
	if err != nil {
		fmt.Fprintf(stderr, "importer run: %v\n", err)
		return exitFailed
	}
	cfg, err := importer.ParseConfigJSON(src.Config)
	if err != nil {
		fmt.Fprintf(stderr, "importer run: source %q: stored config: %v\n", *id, err)
		return exitFailed
	}
	// Secrets come from the environment now and live only in this process.
	var fetch source.FetchOptions
	if cfg.Auth != nil {
		header, value, err := cfg.Auth.Resolve(getenv)
		if err != nil {
			fmt.Fprintf(stderr, "importer run: source %q: %v\n", *id, err)
			return exitUsage
		}
		fetch.Client = authClient(nil, cfg.Location, header, value)
	}

	if !*dry {
		// A hard kill leaves runs 'running'; close those whose source lock is gone.
		ids, err := st.SweepAbandonedRuns(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "importer run: %v\n", err)
			return exitFailed
		}
		if len(ids) > 0 {
			fmt.Fprintf(stderr, "importer run: marked %d abandoned run(s) failed: %v\n", len(ids), ids)
		}
	}

	start := time.Now()
	res, err := importer.Run(ctx, importer.Deps{
		Store:    st,
		Adapters: map[string]source.Adapter{config.KindCSV: source.NewCSV(fetch), config.KindXLSX: source.NewXLSX(fetch)},
		Files:    files,
	}, *id, importer.Options{DryRun: *dry})
	dur := time.Since(start).Round(time.Millisecond)
	if errors.Is(err, importer.ErrAlreadyRunning) {
		fmt.Fprintf(stderr, "importer run: %v\n", err)
		return exitRunning
	}
	if res.Status == "" { // never started a run
		fmt.Fprintf(stderr, "importer run: %v\n", err)
		return exitFailed
	}
	fmt.Fprintln(stdout, summary(*id, res, dur))
	if err != nil {
		fmt.Fprintf(stderr, "importer run: %v\n", err)
		return exitFailed
	}
	return exitOK
}

func summary(id string, r importer.RunResult, d time.Duration) string {
	run := fmt.Sprint(r.RunID)
	if r.DryRun {
		run = "dry-run"
	}
	out := fmt.Sprintf("source=%s status=%s inserted=%d updated=%d unchanged=%d failed=%d run=%s duration=%s",
		id, r.Status, r.Inserted, r.Updated, r.Unchanged, r.Failed, run, d)
	// A source with `resolve` shows the location that was chosen.
	var meta map[string]string
	if json.Unmarshal(r.SourceMeta, &meta) == nil && meta[source.ResolveKey] != "" {
		out += fmt.Sprintf(" location=%q", meta[source.ResolveKey])
	}
	return out
}

// authClient returns an HTTP client that adds header: value to requests to the
// host of location only (never to a redirect target on another host).
func authClient(base http.RoundTripper, location, header, value string) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}
	host := ""
	if u, err := url.Parse(location); err == nil {
		host = u.Host
	}
	return &http.Client{Transport: &authTransport{base: base, host: host, header: header, value: value}}
}

type authTransport struct {
	base         http.RoundTripper
	host, header string
	value        string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == t.host {
		req = req.Clone(req.Context())
		req.Header.Set(t.header, t.value)
	}
	return t.base.RoundTrip(req)
}
