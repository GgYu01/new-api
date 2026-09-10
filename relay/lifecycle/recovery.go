package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

const (
	ClassRecoverablePreHeader     = "pre_header_transport"
	ClassRecoverableNon2xx        = "pre_semantic_non2xx"
	ClassRecoverableSSEDisconnect = "pre_semantic_sse_disconnect"
	ClassClientGone               = "client_gone"
	ClassSemanticCommitted        = "semantic_committed"
	ClassSideEffectUnknown        = "side_effect_executed_unknown"
	ClassSkipRetry                = "skip_retry"
	ClassBudgetExhausted          = "budget_exhausted"
	ClassMaxAttempts              = "max_attempts"
)

func ClassifyTransportError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ClassSkipRetry
	}
	msg := strings.ToLower(err.Error())
	var opErr *net.OpError
	if errors.As(err, &opErr) || errors.Is(err, syscall.ECONNREFUSED) ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection termination") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "tls") && (strings.Contains(msg, "handshake") || strings.Contains(msg, "eof")) ||
		strings.Contains(msg, "goaway") ||
		strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "eof") {
		return ClassRecoverablePreHeader
	}
	return ClassRecoverablePreHeader
}

func ClassifyStatus(code int) string {
	if code == http.StatusTooManyRequests || code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout {
		return ClassRecoverableNon2xx
	}
	if operation_setting.ShouldRetryByStatusCode(code) {
		return ClassRecoverableNon2xx
	}
	return ClassSkipRetry
}

func DrainAndClose(resp *http.Response, limit int) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	if limit <= 0 {
		limit = DefaultDiagnosticBodyLimit
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		_ = resp.Body.Close()
	}()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)))
	if err != nil && !errors.Is(err, io.EOF) {
		return buf
	}
	return buf
}

func ErrorFromTransport(err error) *types.NewAPIError {
	if err == nil {
		return nil
	}
	class := ClassifyTransportError(err)
	status := http.StatusBadGateway
	if class == ClassSkipRetry {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeDoRequestFailed, status, types.ErrOptionWithSkipRetry())
	}
	return types.NewErrorWithStatusCode(err, types.ErrorCodeDoRequestFailed, status)
}

func ErrorFromStatus(resp *http.Response, body []byte) *types.NewAPIError {
	if resp == nil {
		return types.NewErrorWithStatusCode(fmt.Errorf("nil upstream response"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	}
	msg := fmt.Sprintf("bad response status code %d", resp.StatusCode)
	if len(body) > 0 {
		preview := common.LocalLogPreview(string(body))
		if preview != "" {
			msg = fmt.Sprintf("%s: %s", msg, preview)
		}
	}
	return types.NewErrorWithStatusCode(fmt.Errorf("%s", msg), types.ErrorCodeBadResponseStatusCode, resp.StatusCode)
}

func ErrorFromPresemanticDisconnect(err error) *types.NewAPIError {
	if err == nil {
		err = io.EOF
	}
	return types.NewErrorWithStatusCode(
		fmt.Errorf("presemantic upstream disconnect: %w", err),
		types.ErrorCodeBadResponse,
		http.StatusBadGateway,
	)
}

func CanRecover(lr *LogicalRequest, apiErr *types.NewAPIError, retryTimesRemaining int) (bool, string) {
	if apiErr == nil {
		return false, ClassSkipRetry
	}
	if lr == nil {
		return false, ClassSkipRetry
	}
	if lr.rootCtx != nil && lr.rootCtx.Err() != nil {
		return false, ClassClientGone
	}
	if lr.SemanticCommitted() {
		return false, ClassSemanticCommitted
	}
	if lr.TerminalSent() {
		return false, ClassSkipRetry
	}
	if lr.HasSideEffects() && lr.UpstreamMayHaveExecuted() {
		return false, ClassSideEffectUnknown
	}
	if types.IsSkipRetryError(apiErr) {
		return false, ClassSkipRetry
	}
	if operation_setting.IsAlwaysSkipRetryCode(apiErr.GetErrorCode()) {
		return false, ClassSkipRetry
	}
	if lr.RemainingPrecommit() <= 0 {
		return false, ClassBudgetExhausted
	}
	if lr.DispatchedAttempts() >= lr.cfg.MaxUpstreamAttempts {
		return false, ClassMaxAttempts
	}

	code := apiErr.StatusCode
	class := ClassifyStatus(code)
	if code < 100 || code > 599 {
		class = ClassRecoverablePreHeader
	}
	if types.IsChannelError(apiErr) {
		class = ClassRecoverableNon2xx
	}
	msg := strings.ToLower(apiErr.Error())
	if strings.Contains(msg, "presemantic") || strings.Contains(msg, "eof") ||
		strings.Contains(msg, "reset") || strings.Contains(msg, "refused") ||
		strings.Contains(msg, "goaway") {
		class = ClassRecoverablePreHeader
	}
	if class == ClassSkipRetry {
		return false, class
	}
	_ = retryTimesRemaining
	return true, class
}
