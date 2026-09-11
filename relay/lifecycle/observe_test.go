package lifecycle

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestObserveExposesRootAndAttemptFields(t *testing.T) {
	lr := New(context.Background(), "root-obs-1", true, types.RelayFormatOpenAI)
	id, _, err := lr.BeginAttempt()
	require.NoError(t, err)
	require.NotEmpty(t, id)
	lr.EndAttempt(AttemptRecord{ChannelID: 7, TransportPath: "cpa-primary-tls", ErrorClass: "transport_eof_before_headers", StatusCode: 502})

	obs := lr.Observe()
	require.Equal(t, "root-obs-1", obs.RootID)
	require.Equal(t, id, obs.AttemptID)
	require.Equal(t, 0, obs.AttemptIndex)
	require.Equal(t, 1, obs.AttemptCount)
	require.Len(t, obs.Attempts, 1)
	require.Equal(t, 7, obs.Attempts[0].ChannelID)
	require.Equal(t, "cpa-primary-tls", obs.Attempts[0].TransportPath)
	require.Equal(t, "transport_eof_before_headers", obs.Attempts[0].ErrorClass)
	require.Equal(t, 502, obs.Attempts[0].StatusCode)
}

func TestObserveNilIsEmpty(t *testing.T) {
	var lr *LogicalRequest
	obs := Observe(lr)
	require.Empty(t, obs.RootID)
	require.Empty(t, obs.Attempts)
}
