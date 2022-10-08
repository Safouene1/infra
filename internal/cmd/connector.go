package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

type options struct {
	name       string
	accessKey  string
	serverAddr string
}

func newConnectorInstallCmd(cli *CLI) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "install the connector",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnectorInstall(cli)
		},
	}
}

func runConnectorInstall(cli *CLI) error {
	kubeConfig, err := clientConfig().RawConfig()
	if err != nil {
		return err
	}

	// TODO: setup load balancer based on the url of the kubernetes api

	// TODO: error if not current context
	fmt.Fprintf(cli.Stdout, "Installing Infra connector to %v", kubeConfig.CurrentContext)
	// TODO: lookup helm version and print it

	return nil
}

var command = `
helm repo add infrahq https://helm.infrahq.com
helm repo update
helm upgrade --install infra-connector infrahq/infra
   --set connector.config.server=${window.location.host}
   --set connector.config.name=${name}
   --set connector.config.accessKey=${accessKey}`
