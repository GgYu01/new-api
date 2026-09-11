package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/health"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestG010ClassificationMatrix(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		body   string
		class  string
	}{
		{name: "refused", err: syscall.ECONNREFUSED, class: ClassRecoverablePreHeader},
		{name: "EOF", err: io.EOF, class: ClassRecoverablePreHeader},
		{name: "reset", err: syscall.ECONNRESET, class: ClassRecoverablePreHeader},
		{name: "TLS-abort", err: errors.New("tls: handshake aborted"), class: ClassRecoverablePreHeader},
		{name: "GOAWAY", err: errors.New("http2: GOAWAY"), class: ClassRecoverablePreHeader},
		{name: "presemantic-disconnect", class: ClassRecoverableSSEDisconnect},
		{name: "502", status: 502, class: ClassRecoverableNon2xx},
		{name: "503", status: 503, class: ClassRecoverableNon2xx},
		{name: "504", status: 504, class: ClassRecoverableNon2xx},
		{name: "429-usage_limit", status: 429, body: `{"error":{"code":"usage_limit","message":"usage limit"}}`, class: ClassRecoverableNon2xx},
		{name: "Retry-After", status: 503, class: ClassRecoverableNon2xx},
		{name: "overloaded", status: 529, body: `{"error":{"type":"overloaded_error","message":"overloaded"}}`, class: ClassRecoverableNon2xx},
		{name: "upstream-408", status: 408, class: ClassRecoverableNon2xx},
		{name: "520-HTML", status: 520, body: `<html>origin EOF reset</html>`, class: ClassRecoverableNon2xx},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lr := New(context.Background(), tc.name, true, types.RelayFormatOpenAI)
				defer lr.Cleanup()
				var apiErr *types.NewAPIError
				switch {
				case tc.err != nil:
					apiErr = ErrorFromTransport(tc.err)
				case tc.status != 0:
					resp := &http.Response{StatusCode: tc.status, Header: make(http.Header)}
					if tc.name == "Retry-After" {
						resp.Header.Set("Retry-After", "2")
					}
					apiErr = ErrorFromStatus(resp, []byte(tc.body))
				default:
					apiErr = ErrorFromPresemanticDisconnect(io.EOF)
				}
				start := time.Now()
				ok, class := CanRecover(lr, apiErr, 7)
				require.True(t, ok)
				require.Equal(t, tc.class, class)
				if tc.name == "Retry-After" {
					require.Equal(t, 2*time.Second, time.Since(start))
				}
				// Every eligible fault must obey the same transport health cooldown gate.
				table := health.NewTable(time.Second)
				key := health.Key{TransportPathID: tc.name}
				table.MarkUnhealthy(key, time.Now())
				probes := 0
				_, _ = table.WaitOrProbe(context.Background(), key, time.Now(), func(context.Context) error { probes++; return apiErr })
				require.Zero(t, probes, "must not probe within cooldown")
				_, _ = table.WaitOrProbe(context.Background(), key, time.Now().Add(time.Second), func(context.Context) error { probes++; return apiErr })
				require.Equal(t, 1, probes, "one probe at cooldown boundary")
				t.Logf("G010-C002 fault=%s class=%s retryable=%t cooldown_honored=true", tc.name, class, ok)
			})
		})
	}
}

func TestG010NeverReplayBoundaries(t *testing.T) {
	for _, code := range []types.ErrorCode{
		types.ErrorCodeReadRequestBodyFailed, types.ErrorCodeRequestBodyStalled, types.ErrorCodeRequestBodyTruncated,
		types.ErrorCodeBadRequestBody, types.ErrorCodeInvalidRequest, types.ErrorCodeConvertRequestFailed,
		types.ErrorCodeChannelParamOverrideInvalid, types.ErrorCodeModelNotFound, types.ErrorCodeAccessDenied,
		"subscription_required", "permission_denied", types.ErrorCodePromptBlocked, types.ErrorCodeSensitiveWordsDetected,
		"safety_refusal", "encrypted_content", "thinking_signature_invalid", "schema_error", "invalid_parameter",
	} {
		t.Run(string(code), func(t *testing.T) {
			lr := New(context.Background(), string(code), true, types.RelayFormatOpenAI)
			defer lr.Cleanup()
			// A gateway status and misleading transport words must never override a boundary's type.
			apiErr := types.NewErrorWithStatusCode(errors.New("EOF reset refused"), code, 502)
			ok, _ := CanRecover(lr, apiErr, 7)
			require.False(t, ok)
			require.Equal(t, code, apiErr.GetErrorCode())
		})
	}
	for _, status := range []int{400, 401, 403, 404, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			lr := New(context.Background(), "permanent-http", true, types.RelayFormatOpenAI)
			defer lr.Cleanup()
			ok, _ := CanRecover(lr, ErrorFromStatus(&http.Response{StatusCode: status}, []byte(`{"error":{"message":"EOF reset"}}`)), 7)
			require.False(t, ok)
		})
	}
	t.Run("real-client-cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		lr := New(ctx, "cancel", true, types.RelayFormatOpenAI)
		defer lr.Cleanup()
		cancel()
		ok, class := CanRecover(lr, ErrorFromTransport(io.EOF), 7)
		require.False(t, ok)
		require.Equal(t, ClassClientGone, class)
	})
	t.Run("executed-unknown-non-idempotent", func(t *testing.T) {
		lr := New(context.Background(), "side-effect", true, types.RelayFormatOpenAI)
		defer lr.Cleanup()
		lr.MarkHasSideEffects()
		lr.MarkUpstreamMayHaveExecuted()
		ok, class := CanRecover(lr, ErrorFromTransport(io.EOF), 7)
		require.False(t, ok)
		require.Equal(t, ClassSideEffectUnknown, class)
	})
}

func TestG010SessionStateErrorRealSurface(t *testing.T) {
	for _, code := range []string{"encrypted_content", "thinking_signature_invalid"} {
		for _, showBody := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/showBody=%t", code, showBody), func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				lr := New(c.Request.Context(), "session", true, types.RelayFormatOpenAIResponses)
				defer lr.Cleanup()
				Install(c, lr)
				requestBody := []byte(`{"input":[{"encrypted_content":"opaque-state","thinking_signature":"opaque-signature"}]}`)
				storage, err := common.CreateBodyStorage(requestBody)
				require.NoError(t, err)
				defer storage.Close()
				lr.BindBody(storage)
				version := lr.BodyVersion()
				body := fmt.Sprintf(`{"error":{"type":%q,"code":%q,"message":"state invalid"}}`, code, code)
				apiErr := HandleNon2xx(c, &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader(body))}, showBody)
				preserved, message, strip := PreserveSessionStateError(code, body)
				require.Equal(t, code, preserved)
				require.False(t, strip)
				// Guidance is checked semantically through the exported sentinel, not pinned prose.
				require.Equal(t, SessionStateInvalidMessage, message)
				// The controller makes its recovery decision before terminal serialization.
				ok, _ := CanRecover(lr, apiErr, 7)
				require.False(t, ok)
				require.Equal(t, code, fmt.Sprint(apiErr.ToOpenAIError().Code))
				require.Equal(t, code, apiErr.ToOpenAIError().Type)
				require.Equal(t, SessionStateInvalidMessage, apiErr.ToOpenAIError().Message)
				replay, err := lr.NewBodyReader()
				require.NoError(t, err)
				defer replay.Close()
				unchanged, err := io.ReadAll(replay)
				require.NoError(t, err)
				require.Equal(t, requestBody, unchanged)
				require.Equal(t, version, lr.BodyVersion())
			})
		}
	}
}

func TestG010StructuredPermanentErrorOverridesGatewayStatus(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"schema_error","message":"EOF"}}`,
		`{"error":{"code":400,"type":"invalid_request_error","message":"reset"}}`,
		`{"error":{"type":"permission_error"}}`,
		`{"error":{"code":"subscription_required"}}`,
		`{"error":{"code":"safety_refusal"}}`,
		`{"error":{"code":"model_not_found"}}`,
	} {
		lr := New(context.Background(), "structured-boundary", true, types.RelayFormatOpenAI)
		apiErr := ErrorFromStatus(&http.Response{StatusCode: 502}, []byte(body))
		ok, _ := CanRecover(lr, apiErr, 7)
		lr.Cleanup()
		require.False(t, ok, "body=%s", body)
	}
}

func TestG010RetryAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		lr := New(ctx, "cooldown-cancel", true, types.RelayFormatOpenAI)
		defer lr.Cleanup()
		apiErr := ErrorFromStatus(&http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"2"}}}, nil)
		done := make(chan string, 1)
		go func() {
			ok, class := CanRecover(lr, apiErr, 7)
			if ok {
				class = "unexpected-retry"
			}
			done <- class
		}()
		synctest.Wait() // Recovery is durably blocked on the cooldown timer.
		cancel()
		require.Equal(t, ClassClientGone, <-done)
		require.Zero(t, lr.DispatchedAttempts())
	})
}

func TestG010RetryAfterDateAndBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lr := New(context.Background(), "retry-after", true, types.RelayFormatOpenAI)
		defer lr.Cleanup()
		resp := &http.Response{StatusCode: 503, Header: make(http.Header)}
		resp.Header.Set("Retry-After", time.Now().Add(3*time.Second).UTC().Format(http.TimeFormat))
		start := time.Now()
		ok, _ := CanRecover(lr, ErrorFromStatus(resp, nil), 7)
		require.True(t, ok)
		require.Equal(t, 3*time.Second, time.Since(start))
		resp.Header.Set("Retry-After", "1801")
		ok, class := CanRecover(lr, ErrorFromStatus(resp, nil), 7)
		require.False(t, ok)
		require.Equal(t, ClassBudgetExhausted, class)
	})
}
