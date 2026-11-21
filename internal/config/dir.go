package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// File name suffixes of a source directory (see docs/examples/).
const (
	SourceSuffix  = ".source.yaml"
	MappingSuffix = ".mapping.yaml"
)

// SourceFile is one source config with its optional mapping file, as found in a directory.
type SourceFile struct {
	Source      *Source
	SourcePath  string
	MappingPath string // "" when there is no <name>.mapping.yaml next to the source file
	MappingData []byte
}

// LoadDir loads every <name>.source.yaml in dir together with <name>.mapping.yaml.
// Subdirectories are not read. Every problem is collected into the returned
// error (nothing is partially returned): an invalid source, a duplicate source
// id, an unreadable file, or a mapping file with no matching source file.
func LoadDir(dir string) ([]SourceFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sources dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var (
		out   []SourceFile
		errs  []error
		seen  = map[string]string{}
		bases = map[string]bool{}
	)
	for _, n := range names {
		base, ok := strings.CutSuffix(n, SourceSuffix)
		if !ok {
			continue
		}
		bases[base] = true
		sf := SourceFile{SourcePath: filepath.Join(dir, n)}
		src, err := LoadSource(sf.SourcePath)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		sf.Source = src
		if prev, dup := seen[src.ID]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate source id %q (also in %s)", sf.SourcePath, src.ID, prev))
			continue
		}
		seen[src.ID] = sf.SourcePath
		mp := filepath.Join(dir, base+MappingSuffix)
		data, err := os.ReadFile(mp)
		switch {
		case err == nil:
			sf.MappingPath, sf.MappingData = mp, data
		case errors.Is(err, os.ErrNotExist):
		default:
			errs = append(errs, fmt.Errorf("read mapping: %w", err))
			continue
		}
		out = append(out, sf)
	}
	for _, n := range names {
		if base, ok := strings.CutSuffix(n, MappingSuffix); ok && !bases[base] {
			errs = append(errs, fmt.Errorf("%s: mapping file has no matching %s%s", filepath.Join(dir, n), base, SourceSuffix))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sources dir %s: no *%s files found", dir, SourceSuffix)
	}
	return out, nil
}
