package mapping

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Mapping is a parsed mapping document (see docs/mapping-spec.md).
type Mapping struct {
	Source     string            `yaml:"source"`
	Version    int               `yaml:"version"`
	Fields     map[string]*Field `yaml:"fields"`
	Dimensions map[string]*Field `yaml:"dimensions"`
	Extra      string            `yaml:"extra"`
}

// Field is one field spec; it is used for canonical fields and for dimension entries.
type Field struct {
	From        StringList     `yaml:"from"`
	Const       any            `yaml:"const"`
	Template    string         `yaml:"template"`
	EmptyOK     bool           `yaml:"empty_ok"`
	Type        string         `yaml:"type"`
	Format      StringList     `yaml:"format"`
	Precision   string         `yaml:"precision"`
	Transform   StringList     `yaml:"transform"`
	Map         map[string]any `yaml:"map"`
	Default     any            `yaml:"default"`
	Required    bool           `yaml:"required"`
	Validate    string         `yaml:"validate"`
	NullMarkers []string       `yaml:"null_markers"`
	Flag        string         `yaml:"flag"`
	Output      string         `yaml:"output"`
}

// StringList accepts a YAML string or a list of strings.
type StringList []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *StringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var v string
		if err := n.Decode(&v); err != nil {
			return err
		}
		*s = StringList{v}
		return nil
	case yaml.SequenceNode:
		var v []string
		if err := n.Decode(&v); err != nil {
			return err
		}
		*s = StringList(v)
		return nil
	}
	return fmt.Errorf("line %d: expected a string or a list of strings", n.Line)
}
