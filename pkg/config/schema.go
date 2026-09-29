package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

//go:embed schema/config.schema.json
var schemaBytes []byte

const schemaURL = "https://github.com/naviteq/oci-nuke/schema/config.schema.json"

// Validate checks path against the embedded JSON Schema and returns an error naming the
// offending field by JSON-pointer path (CONF-02). It reads the config file and validates it
// as plain data -- it never constructs a common.ConfigurationProvider or an OCI SDK client,
// and this file has no import of the official Oracle Cloud SDK module, satisfying
// CLI-04/CONF-03's "zero OCI API calls" requirement structurally, not by convention.
func Validate(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config %s: %w", path, err)
	}

	var doc interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parsing config %s: %w", path, err)
	}

	compiler := jsonschema.NewCompiler()
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return fmt.Errorf("compiling embedded schema (bug, not a user error): %w", err)
	}
	if err := compiler.AddResource(schemaURL, schemaDoc); err != nil {
		return fmt.Errorf("compiling embedded schema (bug, not a user error): %w", err)
	}
	sch, err := compiler.Compile(schemaURL)
	if err != nil {
		return fmt.Errorf("compiling embedded schema (bug, not a user error): %w", err)
	}

	if err := sch.Validate(doc); err != nil {
		return fmt.Errorf("config validation failed:\n%s", err)
	}

	return nil
}
