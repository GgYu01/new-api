package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	// Permanent request/authentication failures cannot be made retryable by
	// the configurable legacy status ranges or words in an upstream message.
	switch code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusUnprocessableEntity:
		return ClassSkipRetry
	}
	if code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout || code == 520 || code == 529 {
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
	return types.NewErrorWithStatusCode(&RecoveryFault{Err: err, Class: class}, types.ErrorCodeDoRequestFailed, status)
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
	fault := &RecoveryFault{Err: errors.New(msg), Class: ClassifyStatus(resp.StatusCode), RetryAt: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
	var envelope struct {
		Error struct {
			Code any    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if common.Unmarshal(body, &envelope) == nil {
		code, _ := envelope.Error.Code.(string)
		fault.Code = types.ErrorCode(code)
		fault.Type = envelope.Error.Type
		if fault.Code == "" {
			fault.Code = types.ErrorCode(fault.Type)
		}
		if neverReplayCode(fault.Code) || neverReplayCode(types.ErrorCode(fault.Type)) {
			fault.Class = ClassSkipRetry
		}
	}
	code, message, _ := PreserveSessionStateError(string(fault.Code), string(body))
	if message == SessionStateInvalidMessage {
		fault.Code = types.ErrorCode(code)
		fault.Type = code
		fault.Class = ClassSkipRetry
		fault.Err = errors.New(message)
	}
	apiErr := types.NewErrorWithStatusCode(fault, types.ErrorCodeBadResponseStatusCode, resp.StatusCode)
	restoreSessionStateType(apiErr, fault)
	return apiErr
}

func ErrorFromPresemanticDisconnect(err error) *types.NewAPIError {
	if err == nil {
		err = io.EOF
	}
	return types.NewErrorWithStatusCode(
		&RecoveryFault{Err: fmt.Errorf("presemantic upstream disconnect: %w", err), Class: ClassRecoverableSSEDisconnect},
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
	var fault *RecoveryFault
	if errors.As(apiErr, &fault) {
		// HandleNon2xx's diagnostic wrapper retains Err, but can replace the
		// outer code. Restore session guidance before terminal serialization.
		restoreSessionStateType(apiErr, fault)
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
	if neverReplayCode(apiErr.GetErrorCode()) || neverReplayCode(types.ErrorCode(apiErr.ToOpenAIError().Type)) ||
		errors.Is(apiErr, context.Canceled) || errors.Is(apiErr, context.DeadlineExceeded) {
		return false, ClassSkipRetry
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
	if lr.DispatchedAttempts() >= lr.cfg.MaxUpstreamAttempts || lr.DispatchedAttempts() >= DefaultMaxUpstreamAttempts {
		return false, ClassMaxAttempts
	}

	code := apiErr.StatusCode
	class := ClassifyStatus(code)
	if code < 100 || code > 599 || apiErr.GetErrorCode() == types.ErrorCodeDoRequestFailed {
		class = ClassRecoverablePreHeader
	}
	if types.IsChannelError(apiErr) && class != ClassSkipRetry {
		class = ClassRecoverableNon2xx
	}
	if fault != nil {
		class = fault.Class
	}
	if class == ClassSkipRetry {
		return false, class
	}
	if fault != nil {
		if delay := time.Until(fault.RetryAt); delay > 0 {
			if delay >= lr.RemainingPrecommit() || delay >= lr.RemainingLifetime() {
				return false, ClassBudgetExhausted
			}
			// Recovery owns the serial wait; no caller can dispatch through a
			// positive Retry-After. The root heartbeat remains independent.
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-lr.RootContext().Done():
				return false, ClassClientGone
			case <-timer.C:
				return CanRecover(lr, apiErr, retryTimesRemaining)
			}
		}
	}
	_ = retryTimesRemaining
	return true, class
}

// RecoveryFault retains provenance across diagnostic wrappers. Status bodies
// (including HTML containing "EOF") must not masquerade as transport errors.
// RetryAt is an absolute not-before time so repeated decisions do not restart it.
type RecoveryFault struct {
	Err     error
	Class   string
	RetryAt time.Time
	Code    types.ErrorCode
	Type    string
}

func (e *RecoveryFault) Error() string { return e.Err.Error() }
func (e *RecoveryFault) Unwrap() error { return e.Err }

func retryAfter(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		const maxSeconds = uint64((1<<63 - 1) / int64(time.Second))
		if seconds > maxSeconds {
			seconds = maxSeconds
		}
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if date, err := http.ParseTime(value); err == nil {
		return date
	}
	return time.Time{}
}

func neverReplayCode(code types.ErrorCode) bool {
	switch strings.ToLower(string(code)) {
	case string(types.ErrorCodeReadRequestBodyFailed), string(types.ErrorCodeRequestBodyStalled),
		string(types.ErrorCodeRequestBodyTruncated), string(types.ErrorCodeBadRequestBody),
		string(types.ErrorCodeInvalidRequest), string(types.ErrorCodeConvertRequestFailed),
		string(types.ErrorCodeChannelParamOverrideInvalid), string(types.ErrorCodeChannelHeaderOverrideInvalid),
		string(types.ErrorCodeModelNotFound), string(types.ErrorCodeAccessDenied),
		string(types.ErrorCodeDownstreamCanceled), string(types.ErrorCodePromptBlocked),
		string(types.ErrorCodeSensitiveWordsDetected), string(types.ErrorCodeViolationFeeGrokCSAM),
		"invalid_request_error", "invalid_parameter", "schema_error", "permission_denied", "permission_error",
		"subscription_required", "subscription_error", "safety_refusal", "content_policy_violation",
		"encrypted_content", "thinking_signature_invalid":
		return true
	}
	return false
}

func restoreSessionStateType(apiErr *types.NewAPIError, fault *RecoveryFault) {
	code, message, _ := PreserveSessionStateError(string(fault.Code), "")
	if message != SessionStateInvalidMessage {
		return
	}
	// Preserve Err for errors.As, and replace only the client-facing envelope.
	restored := types.WithOpenAIError(types.OpenAIError{Code: code, Type: code, Message: message}, apiErr.StatusCode, types.ErrOptionWithSkipRetry())
	restored.Err = fault
	*apiErr = *restored
}
