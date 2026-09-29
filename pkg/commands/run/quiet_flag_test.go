package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ekristen/libnuke/pkg/queue"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"

	"github.com/naviteq/oci-nuke/pkg/clients"
	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// quietFilterReason is what fakeFilteredResource.Filter reports, and therefore the text
// libnuke's queue.Item.Print writes for the filtered item -- the line --quiet exists to suppress.
const quietFilterReason = "filtered so this test can watch --quiet suppress the line"

// fakeFilteredResource implements libnuke's resource.Filter and always filters itself, which is
// the shortest route to a queue.Item in ItemStateFiltered through a real Nuke.Run (see
// libnuke/pkg/nuke.Nuke.Filter: a non-nil error from the resource's own Filter() sets that state
// and records the error text as the item's Reason).
type fakeFilteredResource struct{}

func (r *fakeFilteredResource) Remove(_ context.Context) error { return nil }
func (r *fakeFilteredResource) UniqueKey() string              { return "fake-filtered-1" }
func (r *fakeFilteredResource) Filter() error                  { return errors.New(quietFilterReason) }

type fakeFilteredLister struct{}

func (l *fakeFilteredLister) List(_ context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}
	return []resource.Resource{&fakeFilteredResource{}}, nil
}

// runQuietScan runs one single-region, single-compartment Nuke over fakeFilteredLister with
// Quiet set as given, and returns everything the run logged plus the queue it produced.
func runQuietScan(t *testing.T, quiet bool) (string, *queue.Queue) {
	t.Helper()

	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)
	registry.Register(&registry.Registration{
		Name:   "FakeFilteredResource",
		Scope:  ocinuke.CompartmentScope,
		Lister: &fakeFilteredLister{},
	})

	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})

	// A logger of this test's own, not the standard one: queue.Item.Print writes through the
	// scanner's logger (libnuke/pkg/scanner sets Item.Logger from it), so passing one here is
	// what makes the printed output observable at all.
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)
	hook := logrustest.NewLocal(logger)

	provider := newFakeProvider(t)
	pr := &pipelineRun{
		ctx:        context.Background(),
		provider:   provider,
		cfg:        testConfig(),
		opts:       pipelineOptions{CompartmentID: testTargetCompartmentID, ForceSleep: minForceSleepSeconds, Quiet: quiet},
		homeRegion: testHomeRegion,
		regions:    []string{testHomeRegion},
		clients:    clients.New(provider, common.DefaultRetryPolicy()),
		logger:     logger,
	}

	tree := scope.BuildTree([]identity.Compartment{
		compartmentFixture(testTargetCompartmentID, "ocid1.compartment.oc1..quietroot"),
	})

	nukes, err := buildNukes(pr, buildParameters(&pr.opts), map[string]struct{}{testTargetCompartmentID: {}}, tree)
	if err != nil {
		t.Fatalf("buildNukes: %v", err)
	}
	if len(nukes) != 1 {
		t.Fatalf("expected exactly 1 Nuke, got %d", len(nukes))
	}
	if err := nukes[0].Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var sb strings.Builder
	for _, entry := range hook.AllEntries() {
		sb.WriteString(entry.Message)
		sb.WriteByte('\n')
		for k, v := range entry.Data {
			fmt.Fprintf(&sb, "%s=%v\n", k, v)
		}
	}
	return sb.String(), nukes[0].Queue
}

func filteredItems(q *queue.Queue) int {
	n := 0
	for _, item := range q.GetItems() {
		if item.Type == "FakeFilteredResource" && item.State == queue.ItemStateFiltered {
			n++
		}
	}
	return n
}

// TestQuiet_SuppressesTheFilteredLineWithoutChangingTheQueue is the assertion CI depends on: the
// shared plan workflow passes --quiet purely to keep the dry-run log readable, so --quiet has to
// change what is printed and nothing else. The run is done twice over an identical scope, and
// the queue is asserted to be identical across both -- a --quiet that also dropped the item
// would turn a hidden resource into an unconsidered one, which is the failure this project's
// safety model cannot afford.
func TestQuiet_SuppressesTheFilteredLineWithoutChangingTheQueue(t *testing.T) {
	loud, loudQueue := runQuietScan(t, false)
	quiet, quietQueue := runQuietScan(t, true)

	if !strings.Contains(loud, quietFilterReason) {
		t.Fatalf("without --quiet the filtered item's reason must be printed; got:\n%s", loud)
	}
	if strings.Contains(quiet, quietFilterReason) {
		t.Fatalf("--quiet must suppress the filtered item's line; got:\n%s", quiet)
	}

	if got := filteredItems(loudQueue); got != 1 {
		t.Fatalf("without --quiet: expected 1 filtered FakeFilteredResource in the queue, got %d", got)
	}
	if got := filteredItems(quietQueue); got != 1 {
		t.Fatalf("with --quiet: expected the same 1 filtered FakeFilteredResource in the queue, "+
			"got %d -- --quiet must hide the line, never the resource", got)
	}
}

// TestParseRunFlags_ReadsQuiet pins --quiet to runFlags.quiet. Registered but unread is exactly
// the shape a flag fails in: cobra accepts the flag either way, and CI would go on passing it
// while nothing acted on it.
func TestParseRunFlags_ReadsQuiet(t *testing.T) {
	cmd := findRunCommand(t)

	flags, err := parseRunFlags(cmd)
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	if flags.quiet {
		t.Fatal("quiet must default to false: the full listing is the default, and CI opts out of it explicitly")
	}

	if err := cmd.Flags().Set("quiet", "true"); err != nil {
		t.Fatalf("setting --quiet: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Flags().Set("quiet", "false") })

	flags, err = parseRunFlags(cmd)
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	if !flags.quiet {
		t.Fatal("--quiet=true must reach runFlags.quiet")
	}
}

// TestBuildParameters_ThreadsQuiet closes the last gap between the flag and libnuke: runFlags
// feeds pipelineOptions, and buildParameters is the only place pipelineOptions.Quiet becomes
// libnuke's own Parameters.Quiet, which is what gates the print.
func TestBuildParameters_ThreadsQuiet(t *testing.T) {
	if buildParameters(&pipelineOptions{}).Quiet {
		t.Fatal("Parameters.Quiet must be false when pipelineOptions.Quiet is unset")
	}
	if !buildParameters(&pipelineOptions{Quiet: true}).Quiet {
		t.Fatal("pipelineOptions.Quiet must reach libnuke's Parameters.Quiet")
	}
}
