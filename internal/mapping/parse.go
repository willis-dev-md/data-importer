package mapping

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Parse decodes a mapping document strictly: unknown keys are errors.
func Parse(data []byte) (*Mapping, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Mapping
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("mapping: empty document")
		}
		return nil, fmt.Errorf("mapping: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("mapping: more than one YAML document")
	}
	for k, f := range m.Fields {
		if f == nil {
			m.Fields[k] = &Field{}
		}
	}
	for k, f := range m.Dimensions {
		if f == nil {
			m.Dimensions[k] = &Field{}
		}
	}
	return &m, nil
}
