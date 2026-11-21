package mapping

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Canonical is the canonical schema (canonical-schema.yaml).
type Canonical struct {
	Version int                        `yaml:"version"`
	Entity  string                     `yaml:"entity"`
	Fields  map[string]*CanonicalField `yaml:"fields"`
}

// CanonicalField declares one canonical field.
type CanonicalField struct {
	Type     string   `yaml:"type"`
	Required bool     `yaml:"required"`
	Nullable bool     `yaml:"nullable"`
	Values   []string `yaml:"values"`
}

var canonicalTypes = map[string]bool{
	"string": true, "int": true, "decimal": true, "bool": true,
	"date": true, "timestamp": true, "enum": true, "map": true,
}

// LoadCanonical reads and checks a canonical schema file.
func LoadCanonical(path string) (*Canonical, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("canonical schema: %w", err)
	}
	return ParseCanonical(data)
}

// ParseCanonical decodes and checks a canonical schema.
func ParseCanonical(data []byte) (*Canonical, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Canonical
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("canonical schema: %w", err)
	}
	if len(c.Fields) == 0 {
		return nil, errors.New("canonical schema: no fields")
	}
	for name, f := range c.Fields {
		if f == nil || !canonicalTypes[f.Type] {
			return nil, fmt.Errorf("canonical schema: field %q has unknown type", name)
		}
		if f.Type == "enum" && len(f.Values) == 0 {
			return nil, fmt.Errorf("canonical schema: enum field %q has no values", name)
		}
	}
	return &c, nil
}
