// Package global provides the persistent flags and pre-run hook shared by every
// oci-nuke command: --log-level and --log-disable-color.
package global

import (
	"fmt"

	liblog "github.com/ekristen/libnuke/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// AddFlags registers the shared --log-level/--log-disable-color persistent flags on cmd
// and wires PersistentPreRunE so every subcommand applies them before running. Every
// command package (version, list, config, and 01-06's run) calls this from its own
// init() so the flags and logging setup are identical everywhere.
func AddFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String("log-level", "info", "log level: trace|debug|info|warn|error")
	cmd.PersistentFlags().Bool("log-disable-color", false, "disable log coloring")
	cmd.PersistentPreRunE = PersistentPreRunE
}

// PersistentPreRunE applies the --log-level/--log-disable-color flags to the standard
// logrus logger. An unparseable --log-level is rejected with an error naming the flag,
// rather than silently falling back to a default level.
func PersistentPreRunE(cmd *cobra.Command, _ []string) error {
	level, _ := cmd.Flags().GetString("log-level")
	disableColor, _ := cmd.Flags().GetBool("log-disable-color")

	logrus.SetFormatter(&liblog.CustomFormatter{
		FallbackFormatter: &logrus.TextFormatter{DisableColors: disableColor},
	})

	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		return fmt.Errorf("invalid --log-level %q: %w", level, err)
	}
	logrus.SetLevel(lvl)
	return nil
}
