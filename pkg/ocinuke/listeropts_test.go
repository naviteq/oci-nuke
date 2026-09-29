package ocinuke

import (
	"errors"
	"testing"

	liberrors "github.com/ekristen/libnuke/pkg/errors"
)

const (
	testOptsTenancyID    = "ocid1.tenancy.oc1..listeropts"
	testOptsCompartment  = "ocid1.compartment.oc1..listeropts"
	testOptsHomeRegion   = "us-ashburn-1"
	testOptsOtherRegion  = "eu-frankfurt-1"
	skipRequestErrorText = "expected a libnuke ErrSkipRequest"
)

// TestBeforeList proves the geography guard skips a Global-geography type outside the home
// region and passes everything else. A plain error here rather than ErrSkipRequest would make
// libnuke count the skip as a listing failure.
func TestBeforeList(t *testing.T) {
	for name, tc := range map[string]struct {
		geo      Geography
		region   string
		wantSkip bool
	}{
		"global in the home region":      {Global, testOptsHomeRegion, false},
		"global outside the home region": {Global, testOptsOtherRegion, true},
		"regional in the home region":    {Regional, testOptsHomeRegion, false},
		"regional outside it":            {Regional, testOptsOtherRegion, false},
	} {
		t.Run(name, func(t *testing.T) {
			opts := &ListerOpts{Region: tc.region, HomeRegion: testOptsHomeRegion}
			assertSkip(t, opts.BeforeList(tc.geo), tc.wantSkip)
		})
	}
}

// TestBeforeListTenancyRoot is NR-775's guard. A tenancy-root-only type must not issue its list
// call for any other compartment: OCI answers 404 NotAuthorizedOrNotFound there, and one error
// line per compartment on every run is what teaches an operator to skim past level=error.
func TestBeforeListTenancyRoot(t *testing.T) {
	for name, tc := range map[string]struct {
		compartmentID string
		wantSkip      bool
	}{
		"the tenancy root itself": {testOptsTenancyID, false},
		"any other compartment":   {testOptsCompartment, true},
	} {
		t.Run(name, func(t *testing.T) {
			opts := &ListerOpts{CompartmentID: tc.compartmentID, TenancyID: testOptsTenancyID}
			assertSkip(t, opts.BeforeListTenancyRoot(), tc.wantSkip)
		})
	}
}

// TestBeforeListTenancyRoot_EmptyTenancyIDDoesNotMatchEmptyCompartment guards the degenerate
// case. Both fields are populated per run, but if a future caller ever left TenancyID empty, a
// naive equality check would report "this IS the root" for an equally empty CompartmentID and
// let the doomed call through. Only a genuinely non-empty match may pass.
func TestBeforeListTenancyRoot_EmptyTenancyIDDoesNotMatchEmptyCompartment(t *testing.T) {
	opts := &ListerOpts{}
	if err := opts.BeforeListTenancyRoot(); err == nil {
		t.Fatal("an empty CompartmentID matched an empty TenancyID and was treated as the tenancy root")
	}
}

func assertSkip(t *testing.T, err error, wantSkip bool) {
	t.Helper()
	if !wantSkip {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal(skipRequestErrorText + ", got nil")
	}
	var skip liberrors.ErrSkipRequest
	if !errors.As(err, &skip) {
		t.Fatalf("%s, got %T: %v", skipRequestErrorText, err, err)
	}
}
