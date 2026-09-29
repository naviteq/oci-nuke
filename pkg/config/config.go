package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads a YAML config file from path and unmarshals it into a Config. It does not
// validate the result against the JSON Schema -- callers that need schema validation should
// call Validate separately (see schema.go).
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	return &c, nil
}
