package list

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/spf13/cobra"
)

// testScope is the registry.Scope value every synthetic registration in this file uses.
const testScope = "compartment"

// testLeafName is the synthetic registration both tests in this file look for by name.
const testLeafName = "Leaf"

// fakeLister is a minimal registry.Lister -- execute() never calls List(), so this only needs to
// satisfy the interface.
type fakeLister struct{}

func (fakeLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return nil, nil
}

// TestExecute_PrintsDependsOnColumn registers two synthetic types directly via
// registry.ClearRegistry()/registry.Register (not ocinuke.Register -- this command reads the
// registry directly and does not need scope wrapping to test its own formatting), one with a
// non-empty DependsOn, and proves `resource-types`' output includes the dependency names for
// that type without depending on any real resource type being registered.
//
// Leaf and OtherLeaf are registered with no DependsOn of their own (so registry.Register's graph
// logic connects them to "root") and Dependent declares DependsOn on both -- mirroring how
// registry.GetNames() actually resolves reachability (see tools/generate-docs's own test for the
// same topsort-from-"root" mechanics): a DependsOn naming a type that was never itself registered
// would leave the dependent type absent from GetNames() entirely.
func TestExecute_PrintsDependsOnColumn(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)

	registry.Register(&registry.Registration{
		Name:     testLeafName,
		Scope:    testScope,
		Resource: &struct{}{},
		Lister:   fakeLister{},
	})
	registry.Register(&registry.Registration{
		Name:     "OtherLeaf",
		Scope:    testScope,
		Resource: &struct{}{},
		Lister:   fakeLister{},
	})
	registry.Register(&registry.Registration{
		Name:      "Dependent",
		Scope:     testScope,
		Resource:  &struct{}{},
		Lister:    fakeLister{},
		DependsOn: []string{"Leaf", "OtherLeaf"},
	})

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := execute(cmd, nil); err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	out := buf.String()

	depLine := lineForName(out, "Dependent")
	if depLine == "" {
		t.Fatalf("could not find output line for %q in:\n%s", "Dependent", out)
	}
	if !strings.Contains(depLine, "Leaf, OtherLeaf") {
		t.Errorf("Dependent's output line = %q, want to contain DependsOn %q", depLine, "Leaf, OtherLeaf")
	}

	leafLine := lineForName(out, testLeafName)
	if leafLine == "" {
		t.Fatalf("could not find output line for %q in:\n%s", testLeafName, out)
	}
	if strings.Contains(leafLine, "Dependent") {
		t.Errorf("Leaf's own output line = %q, must not list DependsOn (it has none)", leafLine)
	}
}

// lineForName returns the first line of out whose leading (name) column exactly equals name, or
// "" if none matches -- a substring search would false-match "Leaf" against "OtherLeaf" or
// against "Dependent"'s own DependsOn column listing "Leaf, OtherLeaf".
func lineForName(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == name {
			return line
		}
	}
	return ""
}

// TestExecute_WritesToStdoutWithNoWriterConfigured is the regression test for the defect the
// test above structurally cannot catch. TestExecute_PrintsDependsOnColumn calls
// cmd.SetOut(&buf) before execute(), which is the one shape production never takes: nothing
// in the real command tree ever sets a writer. Under cobra, cmd.Print* resolves to
// OutOrStderr(), so with no writer set the entire listing went to os.Stderr and
// `oci-nuke resource-types > types.txt` wrote an empty file -- while that test stayed green,
// because setting the writer is exactly what hid the bug.
//
// This test therefore configures NO writer on the command and swaps the process's real
// os.Stdout for a pipe, asserting the listing arrives there. Any future change that routes
// this output back through a stderr-defaulting call fails here.
func TestExecute_WritesToStdoutWithNoWriterConfigured(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)

	registry.Register(&registry.Registration{
		Name:     testLeafName,
		Scope:    testScope,
		Resource: &struct{}{},
		Lister:   fakeLister{},
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	realStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = realStdout })

	// A bare command, exactly as the production tree builds it: no SetOut, no SetErr.
	cmd := &cobra.Command{}
	execErr := execute(cmd, nil)

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	if execErr != nil {
		t.Fatalf("execute() error = %v", execErr)
	}

	if lineForName(string(captured), testLeafName) == "" {
		t.Errorf("registered type %q did not reach os.Stdout; captured stdout = %q.\n"+
			"The listing is going somewhere else (stderr, most likely) -- a user running "+
			"`oci-nuke resource-types > types.txt` gets an empty file.", testLeafName, string(captured))
	}
}
