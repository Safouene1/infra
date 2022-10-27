package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lensesio/tableprinter"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/infrahq/infra/api"
	"github.com/infrahq/infra/internal"
	"github.com/infrahq/infra/internal/cmd/cliopts"
	"github.com/infrahq/infra/internal/cmd/types"
	"github.com/infrahq/infra/internal/connector"
	"github.com/infrahq/infra/internal/logging"
)

// Run the main CLI command with the given args. The args should not contain
// the name of the binary (ex: os.Args[1:]).
func Run(ctx context.Context, args ...string) error {
	cli := newCLI(ctx)
	cmd := NewRootCmd(cli)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}

func mustBeLoggedIn() error {
	if _, ok := os.LookupEnv("INFRA_ACCESS_KEY"); ok {
		// user doesn't need to log in if supplying an access key
		return nil
	}

	config, err := currentHostConfig()
	if err != nil {
		if errors.Is(err, ErrConfigNotFound) {
			return Error{Message: "Not logged in; run 'infra login' before running this command"}
		}
		return fmt.Errorf("getting host config: %w", err)
	}

	// Check expired before checking isLoggedin, since if we check isLoggedIn first, we will never know if it's expired
	if config.isExpired() {
		return Error{Message: "Session expired; run 'infra login' to start a new session"}
	}

	if !config.isLoggedIn() {
		return Error{Message: "Not logged in; run 'infra login' before running this command"}
	}
	return nil
}

func printTable(data interface{}, out io.Writer) {
	table := tableprinter.New(out)

	table.HeaderAlignment = tableprinter.AlignLeft
	table.AutoWrapText = false
	table.DefaultAlignment = tableprinter.AlignLeft
	table.CenterSeparator = ""
	table.ColumnSeparator = ""
	table.RowSeparator = ""
	table.HeaderLine = false
	table.BorderBottom = false
	table.BorderLeft = false
	table.BorderRight = false
	table.BorderTop = false
	table.Print(data)
}

// Creates a new API Client from the current config
func defaultAPIClient() (*api.Client, error) {
	config, err := currentHostConfig()
	if err != nil {
		return nil, err
	}

	server := config.Host
	var accessKey string
	if !config.isExpired() {
		accessKey = config.AccessKey
	}

	if envAccessKey, ok := os.LookupEnv("INFRA_ACCESS_KEY"); ok {
		accessKey = envAccessKey
	}

	if len(accessKey) == 0 {
		if config.isExpired() {
			return nil, Error{Message: "Access key is expired, please `infra login` again", OriginalError: ErrAccessKeyExpired}
		}
		return nil, Error{Message: "Missing access key, must `infra login` or set INFRA_ACCESS_KEY in your environment", OriginalError: ErrAccessKeyMissing}
	}

	if envServer, ok := os.LookupEnv("INFRA_SERVER"); ok {
		server = envServer
	}

	return apiClient(server, accessKey, httpTransportForHostConfig(config)), nil
}

func apiClient(host string, accessKey string, transport *http.Transport) *api.Client {
	return &api.Client{
		Name:      "cli",
		Version:   internal.Version,
		URL:       "https://" + host,
		AccessKey: accessKey,
		HTTP: http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
		OnUnauthorized: logoutCurrent,
	}
}

func logoutCurrent() {
	config, err := readConfig()
	if err != nil {
		logging.Debugf("logging out: read config: %s", err)
		return
	}

	var host *ClientHostConfig
	for i := range config.Hosts {
		if config.Hosts[i].Current {
			host = &config.Hosts[i]
			break
		}
	}

	if host == nil {
		return
	}

	host.AccessKey = ""
	host.Expires = api.Time{}
	host.UserID = 0
	host.Name = ""

	if err := writeConfig(config); err != nil {
		logging.Debugf("logging out: write config: %s", err)
		return
	}
}

func httpTransportForHostConfig(config *ClientHostConfig) *http.Transport {
	pool, err := x509.SystemCertPool()
	if err != nil {
		logging.Warnf("Failed to load trusted certificates from system: %v", err)
		pool = x509.NewCertPool()
	}

	if config.TrustedCertificate != "" {
		ok := pool.AppendCertsFromPEM([]byte(config.TrustedCertificate))
		if !ok {
			logging.Warnf("Failed to read trusted certificates for server")
		}
	}

	return &http.Transport{
		TLSClientConfig: &tls.Config{
			//nolint:gosec // We may purposely set insecureskipverify via a flag
			InsecureSkipVerify: config.SkipTLSVerify,
			RootCAs:            pool,
		},
	}
}

func newUseCmd(cli *CLI) *cobra.Command {
	return &cobra.Command{
		Use:   "use DESTINATION",
		Short: "Access a destination",
		Example: `
# Use a Kubernetes context
$ infra use development

# Use a Kubernetes namespace context
$ infra use development.kube-system`,
		Args:              ExactArgs(1),
		Group:             "Core commands:",
		ValidArgsFunction: getUseCompletion,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := rootPreRun(cmd.Flags()); err != nil {
				return err
			}
			return mustBeLoggedIn()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			destination := args[0]

			client, err := defaultAPIClient()
			if err != nil {
				return err
			}

			config, err := currentHostConfig()
			if err != nil {
				return err
			}

			err = updateKubeConfig(client, config.UserID)
			if err != nil {
				return err
			}

			parts := strings.Split(destination, ".")

			if len(parts) == 1 {
				return kubernetesSetContext(destination, "")
			}

			return kubernetesSetContext(parts[0], parts[1])
		},
	}
}

func getUseCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	client, err := defaultAPIClient()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	_, destinations, grants, err := getUserDestinationGrants(client)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	resources := make(map[string]struct{}, len(grants))

	for _, g := range grants {
		resources[g.Resource] = struct{}{}
	}

	validArgs := make([]string, 0, len(resources))

	for r := range resources {
		var exists bool
		for _, d := range destinations {
			if strings.HasPrefix(r, d.Name) {
				exists = true
				break
			}
		}

		if exists {
			validArgs = append(validArgs, r)
		}

	}

	return validArgs, cobra.ShellCompDirectiveNoSpace

}

func canonicalPath(path string) (string, error) {
	path = os.ExpandEnv(path)

	if strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = strings.Replace(path, "~", homeDir, 1)
	}

	return filepath.Abs(path)
}

func newConnectorCmd() *cobra.Command {
	var configFilename string

	cmd := &cobra.Command{
		Use:    "connector",
		Short:  "Start the Infra connector",
		Args:   NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logging.UseServerLogger()

			options := defaultConnectorOptions()
			err := cliopts.Load(&options, cliopts.Options{
				Filename:  configFilename,
				EnvPrefix: "INFRA_CONNECTOR",
				Flags:     cmd.Flags(),
			})
			if err != nil {
				return err
			}

			// backwards compat for old access key values with a prefix
			accessKey := options.Server.AccessKey.String()
			switch {
			case strings.HasPrefix(accessKey, "file:"):
				filename := strings.TrimPrefix(accessKey, "file:")
				if err := options.Server.AccessKey.Set(filename); err != nil {
					return err
				}
				logging.L.Warn().Msg("accessKey with 'file:' prefix is deprecated. Use the filename without the file: prefix instead.")
			case strings.HasPrefix(accessKey, "env:"):
				key := strings.TrimPrefix(accessKey, "env:")
				options.Server.AccessKey = types.StringOrFile(os.Getenv(key))
				logging.L.Warn().Msg("accessKey with 'env:' prefix is deprecated. Use the INFRA_ACCESS_KEY env var instead.")
			case strings.HasPrefix(accessKey, "plaintext:"):
				options.Server.AccessKey = types.StringOrFile(strings.TrimPrefix(accessKey, "plaintext:"))
				logging.L.Warn().Msg("accessKey with 'plaintext:' prefix is deprecated. Use the literal value without a prefix.")
			}

			// Also accept the same env var as the CLI for setting the access key
			if accessKey, ok := os.LookupEnv("INFRA_ACCESS_KEY"); ok {
				if err := options.Server.AccessKey.Set(accessKey); err != nil {
					return err
				}
			}
			return runConnector(cmd.Context(), options)
		},
	}

	cmd.Flags().StringVarP(&configFilename, "config-file", "f", "", "Connector config file")
	cmd.Flags().StringP("server-url", "s", "", "Infra server hostname")
	cmd.Flags().StringP("server-access-key", "a", "", "Infra access key (use file:// to load from a file)")
	cmd.Flags().StringP("name", "n", "", "Destination name")
	cmd.Flags().String("ca-cert", "", "Path to CA certificate file")
	cmd.Flags().String("ca-key", "", "Path to CA key file")
	cmd.Flags().Bool("server-skip-tls-verify", false, "Skip verifying server TLS certificates")

	return cmd
}

// runConnector is a shim for testing
var runConnector = connector.Run

func defaultConnectorOptions() connector.Options {
	return connector.Options{
		Addr: connector.ListenerOptions{
			HTTPS:   ":443",
			Metrics: ":9090",
		},
	}
}

func NewRootCmd(cli *CLI) *cobra.Command {
	cobra.EnableCommandSorting = false

	rootCmd := &cobra.Command{
		Use:               "infra",
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return rootPreRun(cmd.Flags())
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	// Core commands:
	rootCmd.AddCommand(newLoginCmd(cli))
	rootCmd.AddCommand(newLogoutCmd(cli))
	rootCmd.AddCommand(newListCmd(cli))
	rootCmd.AddCommand(newUseCmd(cli))

	// Management commands:
	rootCmd.AddCommand(newDestinationsCmd(cli))
	rootCmd.AddCommand(newGrantsCmd(cli))
	rootCmd.AddCommand(newUsersCmd(cli))
	rootCmd.AddCommand(newGroupsCmd(cli))
	rootCmd.AddCommand(newKeysCmd(cli))
	rootCmd.AddCommand(newProvidersCmd(cli))

	// Other commands:
	rootCmd.AddCommand(newInfoCmd(cli))
	rootCmd.AddCommand(newVersionCmd(cli))

	// Hidden
	rootCmd.AddCommand(newTokensCmd(cli))
	rootCmd.AddCommand(newServerCmd())
	rootCmd.AddCommand(newConnectorCmd())
	rootCmd.AddCommand(newAgentCmd())
	rootCmd.AddCommand(newSSHConnectorCmd(cli))

	rootCmd.PersistentFlags().String("log-level", "info", "Show logs when running the command [error, warn, info, debug]")
	rootCmd.PersistentFlags().Bool("help", false, "Display help")

	rootCmd.SetHelpCommandGroup("Other commands:")
	rootCmd.AddCommand(newAboutCmd())
	rootCmd.AddCommand(newCompletionsCmd())
	rootCmd.SetUsageTemplate(usageTemplate())
	return rootCmd
}

func rootPreRun(flags *pflag.FlagSet) error {
	if err := cliopts.DefaultsFromEnv("INFRA", flags); err != nil {
		return err
	}
	logLevel, err := flags.GetString("log-level")
	if err != nil {
		return err
	}
	if err := logging.SetLevel(logLevel); err != nil {
		return err
	}
	return nil
}

func addNonInteractiveFlag(flags *pflag.FlagSet, bind *bool) {
	isNonInteractiveMode := os.Stdin == nil || !term.IsTerminal(int(os.Stdin.Fd()))
	flags.BoolVar(bind, "non-interactive", isNonInteractiveMode, "Disable all prompts for input")
}

func addFormatFlag(flags *pflag.FlagSet, bind *string) {
	flags.StringVar(bind, "format", "", "Output format [json|yaml]")
}

func usageTemplate() string {
	return `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{end}}{{range $cmds}}{{if (and (eq .Group "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .Group $group.Group) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`
}
