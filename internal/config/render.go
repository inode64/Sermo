package config

import (
	"encoding/json"
	"fmt"

	"github.com/goccy/go-yaml"
)

// RenderYAML serializes a resolved service tree to YAML.
func RenderYAML(r Resolved) ([]byte, error) {
	data, err := yaml.Marshal(r.Tree)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", r.Name, err)
	}
	return data, nil
}

// RenderJSON serializes a resolved service tree to indented JSON.
func RenderJSON(r Resolved) ([]byte, error) {
	data, err := json.MarshalIndent(r.Tree, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", r.Name, err)
	}
	return data, nil
}
