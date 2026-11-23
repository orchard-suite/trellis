package orrery

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Standard is the mappings for a catalog on the blueprint standard, which
// an estate includes as "standard".
//
//go:embed standard.yaml
var Standard []byte

// ParseMappings reads a mappings file.
func ParseMappings(raw []byte) ([]Mapping, error) {
	var f struct {
		Mappings []Mapping `yaml:"mappings"`
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	for _, m := range f.Mappings {
		if _, err := Compile(m); err != nil {
			return nil, err
		}
	}
	if len(f.Mappings) == 0 {
		return nil, fmt.Errorf("no mappings")
	}
	return f.Mappings, nil
}
