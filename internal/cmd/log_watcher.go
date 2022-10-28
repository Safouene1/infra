package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/infrahq/infra/api"
	"github.com/spf13/cobra"
)

func newAuditCmd(cli *CLI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "stream audit logs",
		Group: "Core commands:",
		RunE: func(cmd *cobra.Command, args []string) error {
		RERUN:
			resp, err := http.Get("http://localhost/api/audit")
			if err != nil {
				fmt.Println("No response from request")
			}
			defer resp.Body.Close()
			var data api.AuditLogs
			dec := json.NewDecoder(resp.Body)
			dec.Decode(&data)

			for _, log := range data.Logs {
				var jsonMap map[string]interface{}
				json.Unmarshal([]byte(log), &jsonMap)
				fmt.Println(jsonMap)
				fmt.Println()
			}

			goto RERUN
			return nil
		},
	}

	return cmd
}
