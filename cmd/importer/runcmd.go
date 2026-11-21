package main

import (
	"context"
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
)

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
	return fmt.Sprintf("source=%s status=%s inserted=%d updated=%d unchanged=%d failed=%d run=%s duration=%s",
		id, r.Status, r.Inserted, r.Updated, r.Unchanged, r.Failed, run, d)
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
