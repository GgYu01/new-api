package lifecycle

import (
	"github.com/gin-gonic/gin"
)

type AttemptObservation struct {
	ID               string `json:"attempt_id"`
	Index            int    `json:"attempt_index"`
	ChannelID        int    `json:"channel_id,omitempty"`
	TransportPath    string `json:"transport_path,omitempty"`
	CredentialAnon   string `json:"credential_anon,omitempty"`
	ErrorClass       string `json:"error_class,omitempty"`
	StatusCode       int    `json:"status_code,omitempty"`
	CancelCause      string `json:"cancel_cause,omitempty"`
	UpstreamExecuted bool   `json:"upstream_may_have_executed,omitempty"`
}

type Observation struct {
	RootID            string               `json:"root_request_id"`
	AttemptID         string               `json:"attempt_id"`
	AttemptIndex      int                  `json:"attempt_index"`
	AttemptCount      int                  `json:"attempt_count"`
	HeadersCommitted  bool                 `json:"headers_committed"`
	KeepaliveOnly     bool                 `json:"keepalive_only"`
	SemanticCommitted bool                 `json:"semantic_committed"`
	TerminalSent      bool                 `json:"terminal_sent"`
	Attempts          []AttemptObservation `json:"attempts"`
}

func (lr *LogicalRequest) Observe() Observation {
	out := Observation{}
	if lr == nil {
		return out
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	out.RootID = lr.RootID
	out.AttemptIndex = int(lr.attemptIndex.Load())
	out.AttemptCount = len(lr.attempts)
	out.HeadersCommitted = lr.headersCommitted.Load()
	out.KeepaliveOnly = lr.keepaliveOnly.Load()
	out.SemanticCommitted = lr.semanticCommitted.Load()
	out.TerminalSent = lr.terminalSent.Load()
	if id, _ := lr.attemptID.Load().(string); id != "" {
		out.AttemptID = id
	}
	for _, a := range lr.attempts {
		out.Attempts = append(out.Attempts, AttemptObservation{
			ID:               a.ID,
			Index:            a.Index,
			ChannelID:        a.ChannelID,
			TransportPath:    a.TransportPath,
			CredentialAnon:   a.CredentialAnon,
			ErrorClass:       a.ErrorClass,
			StatusCode:       a.StatusCode,
			CancelCause:      string(a.CancelCause),
			UpstreamExecuted: a.UpstreamMayExecute,
		})
	}
	return out
}

func ObserveContext(c *gin.Context) Observation {
	return Observe(FromContext(c))
}

func Observe(lr *LogicalRequest) Observation {
	if lr == nil {
		return Observation{}
	}
	return lr.Observe()
}
