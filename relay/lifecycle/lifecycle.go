package lifecycle

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

type ctxKey struct{ name string }

var ginLifecycleKey = ctxKey{"logical_request"}

type CancelCause string

const (
	CancelCauseNone           CancelCause = ""
	CancelCauseClientGone     CancelCause = "client_gone"
	CancelCauseAttemptTimeout CancelCause = "attempt_timeout"
	CancelCauseRootDeadline   CancelCause = "root_deadline"
	CancelCausePrecommit      CancelCause = "precommit_budget"
	CancelCauseSupervisor     CancelCause = "supervisor"
	CancelCauseAttemptEnd     CancelCause = "attempt_end"
)

type AttemptRecord struct {
	ID                 string
	Index              int
	ChannelID          int
	TransportPath      string
	CredentialAnon     string
	ErrorClass         string
	StatusCode         int
	CancelCause        CancelCause
	UpstreamMayExecute bool
	Started            time.Time
	Ended              time.Time
}

// LogicalRequest is the request-owned G2 lifecycle: one root context, serial
// attempt children, a single writer, and once-only cleanup. It does not own
// billing or traffic admission; those stay with existing once middleware.
type LogicalRequest struct {
	mu sync.Mutex

	RootID     string
	rootCtx    context.Context
	rootCancel context.CancelFunc
	cfg        Config
	format     types.RelayFormat
	stream     bool
	clock      func() time.Time

	headersCommitted   atomic.Bool
	keepaliveOnly      atomic.Bool
	semanticCommitted  atomic.Bool
	upstreamMayExecute atomic.Bool
	terminalSent       atomic.Bool
	hasSideEffects     atomic.Bool
	cleaned            atomic.Bool

	writer *Writer
	hb     *heartbeatState

	attemptIndex  atomic.Int32
	attemptID     atomic.Value // string
	attemptCancel context.CancelFunc
	attemptCtx    context.Context

	bodyStorage common.BodyStorage
	bodyVersion uint64

	attempts       []AttemptRecord
	hold           *HoldBuffer
	completionSafe *CompletionSafeMode

	precommitDeadline time.Time
	absoluteDeadline  time.Time
}

func New(root context.Context, rootID string, stream bool, format types.RelayFormat) *LogicalRequest {
	if root == nil {
		root = context.Background()
	}
	if rootID == "" {
		rootID = common.NewRequestId()
	}
	cfg := currentConfig()
	logConfigOnce()
	now := time.Now()
	parent := root
	var lifeCancel context.CancelFunc
	if cfg.LogicalRequestMaxLifetime > 0 {
		parent, lifeCancel = context.WithTimeout(root, cfg.LogicalRequestMaxLifetime)
	}
	ctx, cancel := context.WithCancel(parent)
	rootCancel := func() {
		cancel()
		if lifeCancel != nil {
			lifeCancel()
		}
	}
	lr := &LogicalRequest{
		RootID:            rootID,
		rootCtx:           ctx,
		rootCancel:        rootCancel,
		cfg:               cfg,
		format:            format,
		stream:            stream,
		clock:             time.Now,
		precommitDeadline: now.Add(cfg.PrecommitRecoveryBudget),
		absoluteDeadline:  now.Add(cfg.LogicalRequestMaxLifetime),
	}
	lr.attemptID.Store("")
	initCompletionSafe(lr, format)
	return lr
}

func (lr *LogicalRequest) Config() Config {
	if lr == nil {
		return currentConfig()
	}
	return lr.cfg
}

func (lr *LogicalRequest) RootContext() context.Context {
	if lr == nil {
		return context.Background()
	}
	return lr.rootCtx
}

func (lr *LogicalRequest) AttemptContext() context.Context {
	if lr == nil {
		return context.Background()
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.attemptCtx != nil {
		return lr.attemptCtx
	}
	return lr.rootCtx
}

func (lr *LogicalRequest) Format() types.RelayFormat {
	if lr == nil {
		return types.RelayFormatOpenAI
	}
	return lr.format
}

func (lr *LogicalRequest) IsStream() bool {
	return lr != nil && lr.stream
}

func (lr *LogicalRequest) SetStream(stream bool) {
	if lr == nil {
		return
	}
	lr.stream = stream
}

func (lr *LogicalRequest) SetFormat(format types.RelayFormat) {
	if lr == nil {
		return
	}
	lr.format = format
}

func (lr *LogicalRequest) Writer() *Writer {
	if lr == nil {
		return nil
	}
	return lr.writer
}

func (lr *LogicalRequest) AttachWriter(c *gin.Context) *Writer {
	if lr == nil || c == nil {
		return nil
	}
	if lr.writer != nil {
		return lr.writer
	}
	w := newWriter(c, lr)
	lr.writer = w
	c.Writer = w
	return w
}

func (lr *LogicalRequest) HeadersCommitted() bool {
	return lr != nil && lr.headersCommitted.Load()
}

func (lr *LogicalRequest) KeepaliveOnly() bool {
	return lr != nil && lr.keepaliveOnly.Load()
}

func (lr *LogicalRequest) SemanticCommitted() bool {
	return lr != nil && lr.semanticCommitted.Load()
}

func (lr *LogicalRequest) TerminalSent() bool {
	return lr != nil && lr.terminalSent.Load()
}

func (lr *LogicalRequest) UpstreamMayHaveExecuted() bool {
	return lr != nil && lr.upstreamMayExecute.Load()
}

func (lr *LogicalRequest) HasSideEffects() bool {
	return lr != nil && lr.hasSideEffects.Load()
}

func (lr *LogicalRequest) MarkHasSideEffects() {
	if lr != nil {
		lr.hasSideEffects.Store(true)
	}
}

func (lr *LogicalRequest) MarkHeadersCommitted() {
	if lr == nil {
		return
	}
	lr.headersCommitted.Store(true)
	if !lr.semanticCommitted.Load() {
		lr.keepaliveOnly.Store(true)
	}
}

func (lr *LogicalRequest) MarkKeepaliveOnly() {
	if lr == nil {
		return
	}
	lr.headersCommitted.Store(true)
	if !lr.semanticCommitted.Load() {
		lr.keepaliveOnly.Store(true)
	}
}

func (lr *LogicalRequest) MarkSemanticCommitted() {
	if lr == nil {
		return
	}
	lr.headersCommitted.Store(true)
	lr.semanticCommitted.Store(true)
	lr.keepaliveOnly.Store(false)
}

func (lr *LogicalRequest) MarkUpstreamMayHaveExecuted() {
	if lr != nil {
		lr.upstreamMayExecute.Store(true)
	}
}

func (lr *LogicalRequest) MarkTerminalSent() {
	if lr != nil {
		lr.terminalSent.Store(true)
	}
}

func (lr *LogicalRequest) AttemptID() string {
	if lr == nil {
		return ""
	}
	v, _ := lr.attemptID.Load().(string)
	return v
}

func (lr *LogicalRequest) AttemptIndex() int {
	if lr == nil {
		return 0
	}
	return int(lr.attemptIndex.Load())
}

func (lr *LogicalRequest) DispatchedAttempts() int {
	if lr == nil {
		return 0
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return len(lr.attempts)
}

func (lr *LogicalRequest) RemainingPrecommit() time.Duration {
	if lr == nil {
		return 0
	}
	rem := time.Until(lr.precommitDeadline)
	if rem < 0 {
		return 0
	}
	return rem
}

func (lr *LogicalRequest) RemainingLifetime() time.Duration {
	if lr == nil {
		return 0
	}
	rem := time.Until(lr.absoluteDeadline)
	if rem < 0 {
		return 0
	}
	return rem
}

func (lr *LogicalRequest) AttemptFirstEventTimeout() time.Duration {
	if lr == nil {
		return DefaultAttemptFirstEventTimeout
	}
	limit := lr.cfg.AttemptFirstEventTimeout
	if rem := lr.RemainingLifetime(); rem > 0 && rem < limit {
		limit = rem
	}
	if rem := lr.RemainingPrecommit(); !lr.SemanticCommitted() && rem > 0 && rem < limit {
		limit = rem
	}
	if limit <= 0 {
		return time.Millisecond
	}
	return limit
}

func (lr *LogicalRequest) BindBody(storage common.BodyStorage) {
	if lr == nil {
		return
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.bodyStorage = storage
	if storage != nil {
		lr.bodyVersion++
	}
}

func (lr *LogicalRequest) upstreamBytesLocked() int64 {
	if lr == nil || lr.bodyStorage == nil {
		return -1
	}
	return lr.bodyStorage.Size()
}

func (lr *LogicalRequest) downstreamBytesLocked() int64 {
	if lr == nil || lr.writer == nil {
		return -1
	}
	if n := lr.writer.Size(); n >= 0 {
		return int64(n)
	}
	return 0
}

func (lr *LogicalRequest) BodyVersion() uint64 {
	if lr == nil {
		return 0
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.bodyVersion
}

func (lr *LogicalRequest) NewBodyReader() (io.ReadCloser, error) {
	if lr == nil {
		return nil, fmt.Errorf("logical request is nil")
	}
	lr.mu.Lock()
	storage := lr.bodyStorage
	lr.mu.Unlock()
	if storage == nil {
		return nil, fmt.Errorf("body storage is nil")
	}
	return storage.NewReader()
}

func (lr *LogicalRequest) ObserveRequest(req dto.Request) {
	if lr == nil || req == nil {
		return
	}
	if hasSideEffectTools(req) {
		lr.MarkHasSideEffects()
	}
}

func hasSideEffectTools(req dto.Request) bool {
	switch r := req.(type) {
	case *dto.GeneralOpenAIRequest:
		return len(r.Tools) > 0 || r.Functions != nil
	case *dto.OpenAIResponsesRequest:
		return len(r.Tools) > 0
	case *dto.ClaudeRequest:
		return r.Tools != nil
	default:
		return false
	}
}

// BeginAttempt starts a child context for one upstream try. The root
// Request.Context is never replaced. Caller must EndAttempt before the next
// BeginAttempt.
func (lr *LogicalRequest) BeginAttempt() (attemptID string, attemptCtx context.Context, err error) {
	if lr == nil {
		return "", context.Background(), fmt.Errorf("logical request is nil")
	}
	if lr.rootCtx.Err() != nil {
		return "", lr.rootCtx, lr.rootCtx.Err()
	}
	if lr.SemanticCommitted() {
		return "", lr.rootCtx, fmt.Errorf("semantic output already committed")
	}
	if lr.RemainingPrecommit() <= 0 {
		return "", lr.rootCtx, fmt.Errorf("precommit recovery budget exhausted")
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.attemptCancel != nil {
		return "", lr.rootCtx, fmt.Errorf("previous attempt has not exited")
	}
	if len(lr.attempts) >= lr.cfg.MaxUpstreamAttempts {
		return "", lr.rootCtx, fmt.Errorf("max upstream attempts reached")
	}
	idx := len(lr.attempts)
	id := fmt.Sprintf("%s-a%d", lr.RootID, idx)
	timeout := lr.cfg.AttemptFirstEventTimeout
	if rem := time.Until(lr.absoluteDeadline); rem > 0 && rem < timeout {
		timeout = rem
	}
	if rem := time.Until(lr.precommitDeadline); rem > 0 && rem < timeout {
		timeout = rem
	}
	if timeout <= 0 {
		timeout = time.Millisecond
	}
	ctx, cancel := context.WithTimeout(lr.rootCtx, timeout)
	lr.attemptCancel = cancel
	lr.attemptCtx = ctx
	lr.attemptIndex.Store(int32(idx))
	lr.attemptID.Store(id)
	lr.attempts = append(lr.attempts, AttemptRecord{
		ID:      id,
		Index:   idx,
		Started: lr.clock(),
	})
	return id, ctx, nil
}

func (lr *LogicalRequest) EndAttempt(rec AttemptRecord) {
	if lr == nil {
		return
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.attemptCancel == nil && lr.attemptCtx == nil {
		return
	}
	if lr.attemptCancel != nil {
		lr.attemptCancel()
		lr.attemptCancel = nil
	}
	lr.attemptCtx = nil
	if len(lr.attempts) == 0 {
		return
	}
	last := &lr.attempts[len(lr.attempts)-1]
	if rec.ID != "" {
		last.ID = rec.ID
	}
	last.ChannelID = rec.ChannelID
	last.TransportPath = rec.TransportPath
	last.CredentialAnon = rec.CredentialAnon
	last.ErrorClass = rec.ErrorClass
	last.StatusCode = rec.StatusCode
	last.CancelCause = rec.CancelCause
	last.UpstreamMayExecute = rec.UpstreamMayExecute
	last.Ended = lr.clock()
	if rec.UpstreamMayExecute {
		lr.upstreamMayExecute.Store(true)
	}
	cs := lr.completionSafe
	if cs != nil && !cs.HasCompleted() && !cs.Flushed() {
		cs.Reset()
	}
}

func (lr *LogicalRequest) CancelRoot(cause CancelCause) {
	if lr == nil {
		return
	}
	_ = cause
	if lr.rootCancel != nil {
		lr.rootCancel()
	}
}

func (lr *LogicalRequest) Cleanup() {
	if lr == nil {
		return
	}
	if !lr.cleaned.CompareAndSwap(false, true) {
		return
	}
	lr.StopHeartbeat()
	lr.mu.Lock()
	if lr.attemptCancel != nil {
		lr.attemptCancel()
		lr.attemptCancel = nil
	}
	cs := lr.completionSafe
	lr.mu.Unlock()
	if cs != nil {
		cs.Close()
	}
	if lr.rootCancel != nil {
		lr.rootCancel()
	}
}

func FromContext(c *gin.Context) *LogicalRequest {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(string(constant.ContextKeyLogicalRequest)); ok {
		if lr, ok := v.(*LogicalRequest); ok {
			return lr
		}
	}
	if v, ok := c.Get("logical_request"); ok {
		if lr, ok := v.(*LogicalRequest); ok {
			return lr
		}
	}
	return nil
}

func Install(c *gin.Context, lr *LogicalRequest) {
	if c == nil || lr == nil {
		return
	}
	c.Set(string(constant.ContextKeyLogicalRequest), lr)
	c.Set("logical_request", lr)
	c.Set("root_request_id", lr.RootID)
	c.Header("X-NewAPI-Root-Request-Id", lr.RootID)
}

func FromRequestContext(ctx context.Context) *LogicalRequest {
	if ctx == nil {
		return nil
	}
	if lr, ok := ctx.Value(ginLifecycleKey).(*LogicalRequest); ok {
		return lr
	}
	return nil
}
