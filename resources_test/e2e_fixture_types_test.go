package ocinuke_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/ekristen/libnuke/pkg/registry"

	_ "github.com/naviteq/oci-nuke/resources"
)

// fixtureOutputs is the e2e fixture's outputs.tf, read as text rather than through an HCL
// parser: the only thing needed from it is the list of resource-type names, and adding an HCL
// dependency to this module to read one list would be the more surprising choice.
const fixtureOutputs = "../test/e2e/fixture/outputs.tf"

var (
	expectedTypesBlock = regexp.MustCompile(`(?s)value = concat\(\[(.*?)\]\s*,\s*var\.include_kms_vault \? \[(.*?)\] : \[\]\)`)
	quotedName         = regexp.MustCompile(`"([A-Za-z][A-Za-z0-9]*)"`)
)

// TestE2EFixtureDeclaresOnlyRegisteredResourceTypes ties the e2e fixture's expected_resource_types
// output to the registry it describes. The harness asserts the plan covers every name in that
// list before it deletes anything, so a name the registry does not have makes the assertion
// vacuous -- it can never be missing from a plan, because nothing can ever produce it.
//
// This is not hypothetical. The list's first draft said Object, OkeCluster, OkeNodePool and
// KmsVault; the registry has Bucket's ObjectVersion child, Cluster, NodePool and Vault. Four
// wrong names out of fifteen, none of which any amount of `tofu validate` would notice.
func TestE2EFixtureDeclaresOnlyRegisteredResourceTypes(t *testing.T) {
	raw, err := os.ReadFile(fixtureOutputs)
	if err != nil {
		t.Fatalf("reading the e2e fixture's outputs: %v", err)
	}

	match := expectedTypesBlock.FindSubmatch(raw)
	if match == nil {
		t.Fatalf("%s no longer contains an expected_resource_types concat([...], var.include_kms_vault ? [...] : []) "+
			"in the shape this test reads; update the regexp with the fixture rather than deleting the test", fixtureOutputs)
	}

	registered := make(map[string]struct{})
	for _, name := range registry.GetNames() {
		registered[name] = struct{}{}
	}
	if len(registered) == 0 {
		t.Fatal("the registry is empty; the blank import of the resources package is what populates it")
	}

	// Both halves: the always-seeded types and the vault-only ones. A test that read only the
	// first would let the vault names rot unnoticed, which is precisely where two of the four
	// original mistakes were.
	var names []string
	for _, half := range match[1:] {
		for _, m := range quotedName.FindAllSubmatch(half, -1) {
			names = append(names, string(m[1]))
		}
	}
	if len(names) < 10 {
		t.Fatalf("only found %d resource-type names in %s; the fixture seeds more than that, "+
			"so the regexp is matching too little", len(names), fixtureOutputs)
	}

	for _, name := range names {
		if _, ok := registered[name]; !ok {
			t.Errorf("the e2e fixture expects resource type %q, which is not registered; "+
				"the harness would assert a plan covers a type nothing can ever produce", name)
		}
	}
	t.Logf("checked %d fixture resource-type names against %d registered types", len(names), len(registered))
}
