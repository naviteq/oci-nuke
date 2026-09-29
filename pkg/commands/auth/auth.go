// Package auth registers the `auth export-session` command, which writes the session this
// process authenticated with into an OCI config profile other tools can use.
package auth

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/naviteq/oci-nuke/pkg/commands/global"
	"github.com/naviteq/oci-nuke/pkg/common"
	"github.com/naviteq/oci-nuke/pkg/ociauth"
)

func execute(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	dir, err := flags.GetString("out-dir")
	if err != nil {
		return err
	}
	profile, err := flags.GetString("profile-name")
	if err != nil {
		return err
	}
	authMethod, err := flags.GetString("auth")
	if err != nil {
		return err
	}
	oidcRegion, err := flags.GetString("oidc-region")
	if err != nil {
		return err
	}
	oidcAudience, err := flags.GetString("oidc-audience")
	if err != nil {
		return err
	}

	// The same resolver the run command uses, so an export cannot succeed under credentials a
	// run would have rejected -- and so there is exactly one place that decides what --auth
	// means.
	provider, method, err := ociauth.Resolve(cmd.Context(), ociauth.ResolveOptions{
		Method:           ociauth.Method(authMethod),
		OIDCRegion:       oidcRegion,
		OIDCAudience:     oidcAudience,
		OIDCDomainURL:    os.Getenv(ociauth.EnvOIDCDomainURL),
		OIDCClientID:     os.Getenv(ociauth.EnvOIDCClientID),
		OIDCClientSecret: os.Getenv(ociauth.EnvOIDCClientSecret),
	})
	if err != nil {
		return err
	}

	files, err := ociauth.ExportSession(provider, dir, profile)
	if err != nil {
		return err
	}

	// Paths and an expiry, never the token. This output goes to a CI log.
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "auth_method=%s\n", method)
	fmt.Fprintf(out, "oci_config_file=%s\n", files.ConfigPath)
	fmt.Fprintf(out, "oci_config_profile=%s\n", files.Profile)
	if files.ExpiresAt.IsZero() {
		fmt.Fprintln(out, "session_expires_at=unknown")
	} else {
		fmt.Fprintf(out, "session_expires_at=%s\n", files.ExpiresAt.Format(time.RFC3339))
		fmt.Fprintf(out, "session_expires_in=%s\n", time.Until(files.ExpiresAt).Round(time.Second))
	}
	return nil
}

func init() {
	exportCmd := &cobra.Command{
		Use:   "export-session",
		Short: "write the authenticated session to an OCI config profile for other tools",
		Long: "Exchange credentials exactly as a run would, then write the resulting session token, " +
			"its ephemeral private key and an OCI config profile pointing at both.\n\n" +
			"This exists so a keyless CI run can hand the same session to tools that cannot perform " +
			"the exchange themselves: the OpenTofu OCI provider offers no GitHub-OIDC auth mode, only " +
			"SecurityToken, and neither does the OCI CLI.\n\n" +
			"It writes a credential to disk, which --auth api-key deliberately refuses to do. What is " +
			"written is a short-lived session token rather than a long-lived signing key, at 0600 inside " +
			"a 0700 directory. Point --out-dir at somewhere the runner discards, such as $RUNNER_TEMP, " +
			"never at a real $HOME/.oci -- there is a config file there already and this would overwrite " +
			"it.\n\n" +
			"The OpenTofu OCI provider is fussier than the CLI about where it reads from: it resolves " +
			"its config file to <home>/.oci/config and takes no argument and no environment variable " +
			"for the path, where <home> is TF_HOME_OVERRIDE if set and the passwd entry otherwise -- " +
			"not $HOME. To hand it a session, point TF_HOME_OVERRIDE at a scratch directory and pass " +
			"--out-dir <that directory>/.oci.",
		RunE: execute,
	}
	exportCmd.Flags().String("out-dir", "",
		"directory to write config, session_token and session_key.pem into (required)")
	exportCmd.Flags().String("profile-name", "SESSION", "profile name to write inside the config file")
	exportCmd.Flags().String("auth", "",
		"credential source, same values as on the run command: config-file, instance-principal, oke-workload-identity, github-oidc, api-key")
	exportCmd.Flags().String("oidc-region", "", "OCI region for --auth github-oidc (required; not auto-detected)")
	exportCmd.Flags().String("oidc-audience", "",
		"audience requested from GitHub's OIDC endpoint; defaults to the identity domain's own URL")
	if err := exportCmd.MarkFlagRequired("out-dir"); err != nil {
		panic(err)
	}

	authCmd := &cobra.Command{
		Use:   "auth",
		Short: "credential operations",
	}
	authCmd.AddCommand(exportCmd)

	global.AddFlags(authCmd)
	common.RegisterCommand(authCmd)
}
