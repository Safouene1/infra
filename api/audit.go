package api

type KenesisRecord struct {
	Data string `json:"data"`
}

type AWSKenesisFirehose struct {
	Records   []KenesisRecord `json:"records"`
	RequestID string          `json:"requestID"`
	Timestamp int             `json:"timestamp"`
}

type AWSKenesisFirehoseResponse struct {
	RequestID string `json:"requestID"`
	Timestamp int    `json:"timestamp"`
	// ErrorMessage string `json:"errorMessage"`
}

type AuditLogs struct {
	Logs []string `json:"logs"`
}
