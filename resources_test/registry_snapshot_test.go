package ocinuke_test

import (
	"os"
	"testing"

	"github.com/ekristen/libnuke/pkg/registry"
)

// realRegistrations snapshots every resources/*.go registration -- exactly as this package's
// blank import of github.com/naviteq/oci-nuke/resources (registration_test.go,
// filter_contract_test.go) populates the registry at program init time, before any test runs --
// so restoreRealRegistry can put the registry back into that state after a legacy test in this
// package calls registry.ClearRegistry(). This package has called registry.ClearRegistry() to
// build synthetic fixtures since Phase 3, back when resources/ was empty and clearing it was a
// harmless no-op against real data. Now that resources/ is fully populated and multiple tests in
// this package (registration_test.go, filter_contract_test.go, and docs/resources/generated_test.go
// in the sibling package) depend on the registry reflecting every wave-4 type, an unrestored
// registry.ClearRegistry() call silently breaks every test that runs after it in the same test
// binary -- Go runs every test in one package sequentially in one process, sharing this
// package-level global registry state across the whole run.
var realRegistrations []*registry.Registration

// TestMain captures the real registry snapshot once, before any TestXxx function runs (Go
// guarantees every package's init()s -- including this package's blank import of resources/,
// which calls ocinuke.Register 37+ times -- complete before TestMain's body executes), then hands
// control to the normal test runner.
func TestMain(m *testing.M) {
	for _, name := range registry.GetNames() {
		if reg := registry.GetRegistration(name); reg != nil {
			realRegistrations = append(realRegistrations, reg)
		}
	}
	os.Exit(m.Run())
}

// restoreRealRegistry clears whatever synthetic registrations a test installed and re-registers
// every real resources/*.go registration this package snapshotted in TestMain. Every test helper
// in this package that calls registry.ClearRegistry() must pair it with
// t.Cleanup(restoreRealRegistry) so tests that depend on the real, fully-populated registry
// (registration_test.go, filter_contract_test.go) see it correctly regardless of test run order.
func restoreRealRegistry() {
	registry.ClearRegistry()
	for _, reg := range realRegistrations {
		registry.Register(reg)
	}
}
