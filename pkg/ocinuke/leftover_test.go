package ocinuke_test

import (
	"testing"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

func TestReportLeftover_CallsReporterWhenNonNil(t *testing.T) {
	var got *scope.SkipEvent
	reporter := ocinuke.LeftoverReporter(func(evt *scope.SkipEvent) {
		g := *evt
		got = &g
	})

	want := scope.SkipEvent{
		Reason:        scope.ReasonScheduledDeletion,
		ResourceType:  "KmsVault",
		ResourceID:    "ocid1.vault.oc1..test",
		CompartmentID: "ocid1.compartment.oc1..test",
		Detail:        "scheduled for deletion in 7 days",
	}

	ocinuke.ReportLeftover(reporter, &want)

	if got == nil {
		t.Fatal("ReportLeftover did not invoke a non-nil reporter")
	}
	if *got != want {
		t.Errorf("reporter received %+v, want %+v", *got, want)
	}
}

func TestReportLeftover_NilReporterIsNoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ReportLeftover panicked with a nil reporter: %v", r)
		}
	}()

	ocinuke.ReportLeftover(nil, &scope.SkipEvent{Reason: scope.ReasonRetentionLocked})
}
