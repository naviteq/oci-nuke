package ocinuke_test

import (
	"sync"
	"testing"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// TestCurrentScope_FailsClosedByDefault proves CurrentScope reports nothing in scope before any
// SetRunContext call -- the fail-closed default T-04-02 mitigates. A resource type accidentally
// exercised outside a real run (e.g. a buggy test, or a real resources/*.go init() running
// before runPipeline installs run state) must never appear in-scope by default.
func TestCurrentScope_FailsClosedByDefault(t *testing.T) {
	for _, compartmentID := range []string{
		"", "ocid1.compartment.oc1..test", "ocid1.tenancy.oc1..test",
	} {
		if ocinuke.CurrentScope(compartmentID) {
			t.Errorf("CurrentScope(%q) = true before any SetRunContext call, want false (fail closed)", compartmentID)
		}
	}
}

// TestCurrentReporter_NoOpByDefault proves CurrentReporter does not panic when called before any
// SetRunContext call -- the default reporter is a safe no-op.
func TestCurrentReporter_NoOpByDefault(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CurrentReporter panicked before any SetRunContext call: %v", r)
		}
	}()
	ocinuke.CurrentReporter(&scope.SkipEvent{Reason: scope.ReasonOutOfScope})
}

// TestSetRunContext_InstallsAndRestores proves SetRunContext installs the given inScope/reporter
// functions for CurrentScope/CurrentReporter to delegate to, and that calling the returned
// restore func resets both back to the fail-closed defaults -- proving a later, unrelated call
// (e.g. a test that forgets its own setup) never inherits a previous run's scope.
func TestSetRunContext_InstallsAndRestores(t *testing.T) {
	var reportedEvt *scope.SkipEvent
	inScope := ocinuke.InScope(func(compartmentID string) bool {
		return compartmentID == "ocid1.compartment.oc1..inscope"
	})
	reporter := ocinuke.LeftoverReporter(func(evt *scope.SkipEvent) {
		got := *evt
		reportedEvt = &got
	})

	restore := ocinuke.SetRunContext(inScope, reporter)

	if !ocinuke.CurrentScope("ocid1.compartment.oc1..inscope") {
		t.Error("CurrentScope did not delegate to the installed inScope func for an in-scope compartment")
	}
	if ocinuke.CurrentScope("ocid1.compartment.oc1..other") {
		t.Error("CurrentScope reported true for a compartment the installed inScope func excludes")
	}

	want := scope.SkipEvent{Reason: scope.ReasonBlocklisted, ResourceType: "Instance"}
	ocinuke.CurrentReporter(&want)
	if reportedEvt == nil {
		t.Fatal("CurrentReporter did not delegate to the installed reporter func")
	}
	if *reportedEvt != want {
		t.Errorf("installed reporter received %+v, want %+v", *reportedEvt, want)
	}

	restore()

	if ocinuke.CurrentScope("ocid1.compartment.oc1..inscope") {
		t.Error("CurrentScope still reports true after restore() -- must reset to the fail-closed default")
	}

	reportedEvt = nil
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CurrentReporter panicked after restore(): %v", r)
		}
	}()
	ocinuke.CurrentReporter(&scope.SkipEvent{Reason: scope.ReasonOutOfScope})
	if reportedEvt != nil {
		t.Error("the old reporter was still invoked after restore() -- must reset to the no-op default")
	}
}

// TestSetRunContext_ConcurrentAccessIsRaceFree proves CurrentScope/CurrentReporter can be read
// from many concurrently-running goroutines while SetRunContext is installed without a data race
// (go test -race) -- this indirection is read from every concurrently-running scanner goroutine
// and every concurrent Remove() call during a real run (T-04-03).
func TestSetRunContext_ConcurrentAccessIsRaceFree(t *testing.T) {
	inScope := ocinuke.InScope(func(compartmentID string) bool { return compartmentID != "" })
	reporter := ocinuke.LeftoverReporter(func(_ *scope.SkipEvent) {})
	restore := ocinuke.SetRunContext(inScope, reporter)
	defer restore()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ocinuke.CurrentScope("ocid1.compartment.oc1..test")
		}()
		go func() {
			defer wg.Done()
			ocinuke.CurrentReporter(&scope.SkipEvent{Reason: scope.ReasonOutOfScope})
		}()
	}
	wg.Wait()
}
