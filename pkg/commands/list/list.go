// Package list registers the `resource-types` command, which lists every resource type
// registered with libnuke's registry.
package list

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/spf13/cobra"

	"github.com/naviteq/oci-nuke/pkg/commands/global"
	"github.com/naviteq/oci-nuke/pkg/common"

	_ "github.com/naviteq/oci-nuke/resources" // blank-imported so this command reflects every registered type
)

func execute(cmd *cobra.Command, _ []string) error {
	names := registry.GetNames()
	sort.Strings(names)

	// fmt.Fprintf(cmd.OutOrStdout()) rather than cmd.Printf: cobra's Print family resolves to
	// OutOrStderr(), which falls back to os.Stderr whenever no writer was set -- and nothing in
	// the production command tree sets one. That put this entire listing on stderr, so
	// `oci-nuke resource-types > types.txt` wrote an empty file. OutOrStdout() keeps the
	// SetOut-based tests capturing exactly as before while defaulting to stdout, where a
	// listing belongs.
	out := cmd.OutOrStdout()
	for _, name := range names {
		reg := registry.GetRegistration(name)
		if reg.AlternativeResource != "" {
			fmt.Fprintf(out, "%-55s -> %s (alternative resource)\n", name, reg.AlternativeResource)
			continue
		}
		fmt.Fprintf(out, "%-55s %-12s %s\n", name, reg.Scope, strings.Join(reg.DependsOn, ", "))
	}

	return nil
}

func init() {
	cmd := &cobra.Command{
		Use:     "resource-types",
		Aliases: []string{"list-resources"},
		Short:   "list every registered resource type",
		RunE:    execute,
	}
	global.AddFlags(cmd)
	common.RegisterCommand(cmd)
}
