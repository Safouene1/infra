package server

import (
	"bytes"
	"compress/gzip"
	b64 "encoding/base64"
	"fmt"
	"io/ioutil"

	"github.com/gin-gonic/gin"
	"github.com/infrahq/infra/api"
	"github.com/infrahq/infra/internal/logging"
)

var hookAuditLogsRoute = route[api.AWSKenesisFirehose, *api.AWSKenesisFirehoseResponse]{
	handler: AuditLogHook,
	routeSettings: routeSettings{
		omitFromTelemetry:          true,
		omitFromDocs:               true,
		infraVersionHeaderOptional: true,
		authenticationOptional:     true,
		organizationOptional:       true,
	},
}

var getAuditLogsRoute = route[api.EmptyRequest, *api.AuditLogs]{
	handler: AuditLogDumper,
	routeSettings: routeSettings{
		omitFromTelemetry:          true,
		omitFromDocs:               true,
		infraVersionHeaderOptional: true,
		authenticationOptional:     true,
		organizationOptional:       true,
	},
}

var logs = []string{}

func AuditLogHook(c *gin.Context, r *api.AWSKenesisFirehose) (*api.AWSKenesisFirehoseResponse, error) {
	for _, record := range r.Records {
		sDec, _ := b64.StdEncoding.DecodeString(record.Data)
		reader := bytes.NewReader([]byte(sDec))
		gzreader, err := gzip.NewReader(reader)
		if err != nil {
			logging.L.Info().Msgf("failed to create reader: %s", err)
		} else {
			output, err := ioutil.ReadAll(gzreader)
			if err != nil {
				logging.L.Info().Msgf("failed to read data: %s", err)
			}

			logs = append(logs, string(output))

			fmt.Printf("%s", string(output))
		}
	}

	resp := &api.AWSKenesisFirehoseResponse{
		RequestID: r.RequestID,
		Timestamp: r.Timestamp,
	}

	return resp, nil
}

func AuditLogDumper(c *gin.Context, r *api.EmptyRequest) (*api.AuditLogs, error) {
	result := &api.AuditLogs{
		Logs: logs,
	}

	logs = []string{}

	return result, nil
}
