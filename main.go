// Command oci-nuke assembles the full CLI: every pkg/commands/* subcommand self-registers via
// pkg/common.RegisterCommand from its own init(), so main only needs to range over
// pkg/common.GetCommands() and add each one to the root cobra command.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/naviteq/oci-nuke/pkg/common"

	_ "github.com/naviteq/oci-nuke/pkg/commands/auth"
	_ "github.com/naviteq/oci-nuke/pkg/commands/config"
	_ "github.com/naviteq/oci-nuke/pkg/commands/list"
	"github.com/naviteq/oci-nuke/pkg/commands/run"
	_ "github.com/naviteq/oci-nuke/pkg/commands/version"
)

func main() {
	rootCmd := &cobra.Command{
		Use:           "oci-nuke",
		Short:         "Remove every resource in an OCI compartment subtree",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	for _, cmd := range common.GetCommands() {
		rootCmd.AddCommand(cmd)
	}

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)

		// run.ExitError (03-RESEARCH.md Q9) gives clean/refused/leftover-bearing/internal-error
		// outcomes distinct, documented exit codes. Every error NOT wrapped in run.ExitError
		// falls through to the os.Exit(1) fallback below, unchanged from before this phase.
		var exitErr *run.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		os.Exit(1)
	}
}
