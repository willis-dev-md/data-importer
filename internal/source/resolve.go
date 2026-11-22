package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/willis-dev-md/data-importer/internal/config"
)

// Errors of location resolution.
var (
	// ErrNoCandidate means no generated URL exists: every candidate answered "not found". The
	// message lists every URL tried.
	ErrNoCandidate = errors.New("resolve: no candidate location exists")
	// ErrResolveProbe means a candidate could not be checked (5xx, network error, 401/403, ...). That is
	// an error, never "not found": an outage must not make the importer pick an older file.
	ErrResolveProbe = errors.New("resolve: probing a candidate failed")
)

// ResolveKey is the key under which the chosen location is stored in the run's source metadata
// (import_runs.source_meta).
const ResolveKey = "_location"

// Candidates returns the locations `resolve` generates for cfg at time now, newest first. A source without
// `resolve` has one candidate: its location. Months are calendar months in UTC.
func Candidates(cfg *config.Source, now time.Time) ([]string, error) {
	if len(cfg.Resolve) == 0 {
		return []string{cfg.Location}, nil
	}
	names := make([]string, 0, len(cfg.Resolve))
	for n := range cfg.Resolve {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) != 1 {
		return nil, fmt.Errorf("resolve: exactly one placeholder is supported, got %d", len(names))
	}
	name := names[0]
	r := cfg.Resolve[name]
	if r.Kind != config.ResolveMonth {
		return nil, fmt.Errorf("resolve.%s.kind: unsupported kind %q", name, r.Kind)
	}
	start, ok := r.StartMonths()
	if !ok || r.Back < 1 || r.Back > config.MaxResolveBack {
		return nil, fmt.Errorf("resolve.%s: invalid start %q or back %d", name, r.Start, r.Back)
	}
	u := now.UTC()
	first := time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC) // day 1: month arithmetic cannot overflow
	out := make([]string, 0, r.Back)
	for i := 0; i < r.Back; i++ {
		v := r.FormatMonth(first.AddDate(0, start-i, 0))
		out = append(out, strings.ReplaceAll(cfg.Location, "{"+name+"}", v))
	}
	return out, nil
}

// ResolveLocation returns the location to fetch: cfg.Location itself without `resolve`, otherwise the first
// candidate (newest first, clock opts.Now) that exists. Existence is probed with HEAD, falling back to a GET of
// the first byte when HEAD is not allowed (405/501); only 404/410 mean "not found" and move on to the next
// candidate, any other failure (5xx included) is returned as an error naming the URL. Local paths and file://
// URLs are checked with stat.
func ResolveLocation(ctx context.Context, cfg *config.Source, opts FetchOptions) (string, error) {
	if len(cfg.Resolve) == 0 {
		return cfg.Location, nil
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	cands, err := Candidates(cfg, now())
	if err != nil {
		return "", err
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{}
	}
	c := httpsOnly(client)
	if opts.Timeout > 0 {
		c.Timeout = opts.Timeout
	} else if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	var tried []string
	for _, cand := range cands {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("resolve: %w", err)
		}
		found, status, err := probe(ctx, c, cand)
		if err != nil {
			tried = append(tried, cand+" ("+status+")")
			return "", fmt.Errorf("%w: %s: %v (tried, newest first: %s)", ErrResolveProbe, cand, err, listTried(tried))
		}
		tried = append(tried, cand+" ("+status+")")
		if found {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%w; tried, newest first: %s", ErrNoCandidate, listTried(tried))
}

// listTried renders the URLs tried for an error: at most maxListedMembers entries of maxListedNameLen
// characters each, then an "and N more" marker.
func listTried(tried []string) string {
	var b strings.Builder
	for i, n := range tried {
		if i == maxListedMembers {
			fmt.Fprintf(&b, ", ... and %d more", len(tried)-i)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		if r := []rune(n); len(r) > maxListedNameLen {
			n = string(r[:maxListedNameLen]) + "..."
		}
		b.WriteString(n)
	}
	return b.String()
}

// probe reports whether location exists. status is a short description for error messages.
func probe(ctx context.Context, c *http.Client, location string) (found bool, status string, err error) {
	switch {
	case strings.HasPrefix(location, "https://"):
	case strings.HasPrefix(location, "file://"):
		u, perr := url.Parse(location)
		if perr != nil {
			return false, "invalid URL", perr
		}
		return statExists(u.Path)
	case strings.Contains(location, "://"):
		return false, "unsupported scheme", fmt.Errorf("unsupported scheme (use a path, file:// or https://)")
	default:
		return statExists(location)
	}
	code, err := probeRequest(ctx, c, http.MethodHead, location)
	if err != nil {
		return false, "request failed", err
	}
	if code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented {
		code, err = probeRequest(ctx, c, http.MethodGet, location) // ranged: only the first byte is asked for
		if err != nil {
			return false, "request failed", err
		}
		status = fmt.Sprintf("HEAD not allowed, GET %d", code)
		switch code {
		case http.StatusOK, http.StatusPartialContent:
			return true, status, nil
		case http.StatusNotFound, http.StatusGone:
			return false, status, nil
		}
		return false, status, fmt.Errorf("unexpected HTTP status %d (not treated as \"not found\")", code)
	}
	status = fmt.Sprintf("HTTP %d", code)
	switch code {
	case http.StatusOK:
		return true, status, nil
	case http.StatusNotFound, http.StatusGone:
		return false, status, nil
	}
	return false, status, fmt.Errorf("unexpected HTTP status %d (not treated as \"not found\")", code)
}

func statExists(path string) (bool, string, error) {
	st, err := os.Stat(path)
	switch {
	case err == nil && st.Mode().IsRegular():
		return true, "exists", nil
	case err == nil:
		return false, "not a regular file", nil
	case errors.Is(err, os.ErrNotExist):
		return false, "missing", nil
	}
	return false, "stat failed", err
}

func probeRequest(ctx context.Context, c *http.Client, method, location string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, location, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if method == http.MethodGet {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close() // the body is never read
	return resp.StatusCode, nil
}
