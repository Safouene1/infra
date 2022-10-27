package cmd

import (
	"fmt"
	"log/syslog"
	"net/http"

	"github.com/infrahq/infra/api"
	"github.com/infrahq/infra/internal"
	"github.com/spf13/cobra"
)

var l *syslog.Writer

func init() {
	l, _ = syslog.New(syslog.LOG_AUTH|syslog.LOG_WARNING, "infra-ssh")
}

func newSSHConnectorCmd(cli *CLI) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "ssh-connector",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// %f %k %t %u
			fingerprint := args[0]
			_ = args[1] // base64 encoded pub key
			keyType := args[2]
			username := args[3]

			l.Info("starting ssh-connector")
			l.Info(fmt.Sprintf("FP=%v KeyType=%v Username=%v", fingerprint, keyType, username))

			accessKey := "abcdfghedf.010101010101010101010101"

			client := api.Client{
				Name:    "connector/ssh",
				Version: internal.FullVersion(),
				URL:     "https://localhost:10888",
				HTTP: http.Client{
					Transport: httpTransportForHostConfig(&ClientHostConfig{SkipTLSVerify: true}),
				},
				AccessKey: accessKey,
			}

			users, err := client.ListUsers(api.ListUsersRequest{PubKeyFingerprint: fingerprint})
			if err != nil {
				l.Warning(fmt.Sprintf("list users error: %v", err))
				return err
			}
			l.Warning(fmt.Sprintf("number of users for pub key %d", len(users.Items)))
			if len(users.Items) != 1 {
				return fmt.Errorf("wrong number of users found %d", len(users.Items))
			}

			user := users.Items[0]
			l.Info(fmt.Sprintf("user=%v (%v) pub keys %v", user.Name, user.ID, user.PublicKeys))

			// TODO: check the local username matches the infra user with this pub key

			// TODO: check grants allow access to the destination

			for _, key := range user.PublicKeys {
				// TODO: API should store key type in separate field
				cli.Output(key.Key)

				l.Info(fmt.Sprintf("print: %v", key.Key))
			}
			return nil
		},
	}
	return cmd
}
