package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test_SharedRelayClients_TimeoutZeroUnlessRELAY_TIMEOUT documents the G007-C003
// contract for relay traffic: http.Client.Timeout is an absolute wall-clock
// deadline covering connect+headers+body. A non-zero value would kill a 60m
// logical request even while bytes are still flowing.
//
// RELAY_TIMEOUT (common.RelayTimeout, seconds) is an operational override.
// Default is 0 (unset). Operators may set it only as a last-resort kill switch;
// the production path uses phase contexts (logical-request lifetime,
// precommit budget, first-event, stream no-progress) plus Transport Dial/TLS
// timeouts from common.LoadTimeoutLadder().
func Test_SharedRelayClients_TimeoutZeroUnlessRELAY_TIMEOUT(t *testing.T) {
	prev := common.RelayTimeout
	t.Cleanup(func() { common.RelayTimeout = prev })

	common.RelayTimeout = 0
	InitHttpClient()
	t.Cleanup(ResetProxyClientCache)

	direct := GetHttpClient()
	require.NotNil(t, direct)
	assert.Equal(t, time.Duration(0), direct.Timeout, "shared relay client must not set absolute Timeout when RELAY_TIMEOUT is unset")

	protected := newProtectedFetchHTTPClient()
	require.NotNil(t, protected)
	assert.Equal(t, time.Duration(0), protected.Timeout, "SSRF-protected fetch client inherits RELAY_TIMEOUT; default remains 0")

	proxyClient, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{})
	require.NoError(t, err)
	require.NotNil(t, proxyClient)
	assert.Equal(t, time.Duration(0), proxyClient.Timeout)
	assert.Same(t, direct, proxyClient)

	http1Client, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{HTTPProtocol: dto.HTTPProtocolHTTP1})
	require.NoError(t, err)
	require.NotNil(t, http1Client)
	assert.Equal(t, time.Duration(0), http1Client.Timeout)

	common.RelayTimeout = 7
	override := newRelayHTTPClient(http.DefaultTransport)
	assert.Equal(t, 7*time.Second, override.Timeout, "RELAY_TIMEOUT is an operational override, not the default")

	protectedOverride := newProtectedFetchHTTPClient()
	assert.Equal(t, 7*time.Second, protectedOverride.Timeout)
}

func Test_RelayHTTPClientFactory_leavesTimeoutZero(t *testing.T) {
	prev := common.RelayTimeout
	common.RelayTimeout = 0
	t.Cleanup(func() { common.RelayTimeout = prev })

	client := newRelayHTTPClient(http.DefaultTransport)
	assert.Equal(t, time.Duration(0), client.Timeout)
}
