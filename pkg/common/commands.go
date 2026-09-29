// Package common holds cross-cutting scaffolding shared by every command package:
// the self-registration pattern (RegisterCommand/GetCommands) and the build-time
// version identifiers (see version.go).
package common

import "github.com/spf13/cobra"

// commands accumulates every cobra command registered via RegisterCommand. Each
// command package (version, list, config, and later run) calls RegisterCommand from
// its own init(), so main.go only needs to range over GetCommands() and never has to
// import each command package's symbols directly.
var commands []*cobra.Command

// RegisterCommand appends cmd to the set of top-level commands main.go wires onto the
// root cobra.Command.
func RegisterCommand(cmd *cobra.Command) {
	commands = append(commands, cmd)
}

// GetCommands returns every command registered so far via RegisterCommand.
func GetCommands() []*cobra.Command {
	return commands
}
