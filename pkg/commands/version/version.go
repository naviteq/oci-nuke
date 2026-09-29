// Package version registers the `version` command, which prints the build-time
// version/commit/date identifiers from pkg/common.
package version

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/naviteq/oci-nuke/pkg/common"
)

func init() {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "print the version, commit, and build date",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// cmd.OutOrStdout(), not cmd.Println: cobra's Print family falls back to
			// os.Stderr when no writer is set, which put `oci-nuke version` on stderr and
			// made `oci-nuke version > v.txt` write nothing.
			fmt.Fprintln(cmd.OutOrStdout(), common.VersionString())
			return nil
		},
	}
	common.RegisterCommand(cmd)
}
