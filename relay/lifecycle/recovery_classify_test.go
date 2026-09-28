package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyTransportErrorPreHeaderFaults(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"refused", errors.New("dial tcp: connection refused")},
		{"reset", errors.New("read: connection reset by peer")},
		{"termination", errors.New("reset reason: connection termination")},
		{"broken pipe", errors.New("write: broken pipe")},
		{"timeout", errors.New("dial tcp: i/o timeout")},
		{"tls handshake", errors.New("tls: handshake failure")},
		{"tls eof", errors.New("tls: unexpected EOF reading trailer")},
		{"goaway", errors.New("http2: server sent GOAWAY")},
		{"unexpected eof", errors.New("unexpected EOF")},
		{"plain eof", errors.New("EOF")},
		{"syscall refused", syscall.ECONNREFUSED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, ClassRecoverablePreHeader, ClassifyTransportError(tc.err))
		})
	}
}

func TestClassifyTransportErrorSkipsClientCancel(t *testing.T) {
	assert.Equal(t, ClassSkipRetry, ClassifyTransportError(context.Canceled))
	assert.Equal(t, ClassSkipRetry, ClassifyTransportError(context.DeadlineExceeded))
	assert.Equal(t, "", ClassifyTransportError(nil))
}

func TestClassifyStatusRecoverableCodes(t *testing.T) {
	for _, code := range []int{
		http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		assert.Equal(t, ClassRecoverableNon2xx, ClassifyStatus(code), "code %d", code)
	}
}

func TestErrorFromTransportIsRetryableForPreHeader(t *testing.T) {
	apiErr := ErrorFromTransport(errors.New("connection refused"))
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
}
