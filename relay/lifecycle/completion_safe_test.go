package lifecycle

import (
	"strings"
	"testing"
)

func TestCompletionSafeDiscardsFailedAttemptAndReplaysCompleted(t *testing.T) {
	mode := NewCompletionSafeMode(true, 4096)
	mode.Observe(`data: {"type":"response.created","response":{"id":"resp_fail"}}`)
	mode.Reset()
	mode.Observe(`data: {"type":"response.created","response":{"id":"resp_ok"}}`)
	mode.Observe(`data: {"type":"response.output_text.delta","delta":"hi"}`)
	mode.Observe(`data: {"type":"response.completed","response":{"id":"resp_ok"}}`)
	replay := strings.Join(mode.Replay(), "\n")
	if strings.Contains(replay, "resp_fail") {
		t.Fatalf("failed attempt leaked: %s", replay)
	}
	if !strings.Contains(replay, "resp_ok") || !strings.Contains(replay, "response.completed") {
		t.Fatalf("missing successful replay: %s", replay)
	}
}

func TestCompletionSafeDisabledForToolSideEffects(t *testing.T) {
	mode := NewCompletionSafeMode(true, 4096)
	mode.Observe(`data: {"type":"response.output_item.added","item":{"type":"function"}`)
	if mode.Enabled() {
		t.Fatal("tool side-effect requests must fall back to realtime")
	}
}

func TestCompletionSafeDefaultOff(t *testing.T) {
	mode := NewCompletionSafeMode(false, 4096)
	mode.Observe(`data: {"type":"response.created","response":{"id":"resp_x"}}`)
	if len(mode.Replay()) != 0 {
		t.Fatal("default-off mode must not spool")
	}
}
