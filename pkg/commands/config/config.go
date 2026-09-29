// Package config registers the `config` command and its `validate` subcommand, which
// checks a config file against pkg/config's embedded JSON Schema. This command never
// imports the Oracle Cloud SDK -- it only calls into pkg/config.Validate, which is
// itself structurally proven not to make any OCI API calls (see pkg/config/schema_test.go
// TestNoOCISDKImport).
package config

import (
	"fmt"

	"github.com/spf13/cobra"

	ociconfig "github.com/naviteq/oci-nuke/pkg/config"

	"github.com/naviteq/oci-nuke/pkg/commands/global"
	"github.com/naviteq/oci-nuke/pkg/common"
)

func init() {
	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "validate a config file against the config schema",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _ := cmd.Flags().GetString("config")
			if err := ociconfig.Validate(path); err != nil {
				return err
			}
			// cmd.OutOrStdout(), not cmd.Println: cobra's Print family falls back to
			// os.Stderr when no writer is set. A CI step gating on this command's stdout
			// saw nothing at all.
			fmt.Fprintln(cmd.OutOrStdout(), "config is valid")
			return nil
		},
	}
	validateCmd.Flags().String("config", "config.yaml", "path to config file")

	configCmd := &cobra.Command{
		Use:   "config",
		Short: "config-file operations",
	}
	configCmd.AddCommand(validateCmd)

	global.AddFlags(configCmd)
	common.RegisterCommand(configCmd)
}
