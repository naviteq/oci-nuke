//go:build tools

// Package main (tools.go) pins dependencies that Task 1 of this plan `go get`-ed for later
// plans in this phase (01-02 through 01-06) to import directly, but which no Task 3 code
// imports yet. Without an anchor import, `go mod tidy` prunes an unimported dependency from
// both go.mod and go.sum -- these blank imports keep the module graph (including transitive
// hashes) fully resolved so downstream plans never need to run `go get`/`go mod tidy`
// themselves, avoiding go.sum churn across parallel waves. The "tools" build tag is never
// enabled by a normal build or test invocation, so this file contributes nothing to the
// compiled binary.
package main

import (
	_ "github.com/santhosh-tekuri/jsonschema/v6"
	_ "github.com/spf13/cobra"
	_ "gopkg.in/yaml.v3"
)
