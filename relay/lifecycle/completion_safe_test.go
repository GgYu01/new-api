package lifecycle

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
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
	if mode.Fallback() != FallbackRealtime {
		t.Fatalf("fallback=%s", mode.Fallback())
	}
}

func TestCompletionSafeDefaultOff(t *testing.T) {
	mode := NewCompletionSafeMode(false, 4096)
	mode.Observe(`data: {"type":"response.created","response":{"id":"resp_x"}}`)
	if len(mode.Replay()) != 0 {
		t.Fatal("default-off mode must not spool")
	}
}

func TestCompletionSafeEnabledEnvDefaultOff(t *testing.T) {
	os.Unsetenv("COMPLETION_SAFE")
	os.Unsetenv("COMPLETION_SAFE_ENABLED")
	if CompletionSafeEnabled() {
		t.Fatal("completion-safe must be disabled by default")
	}
	t.Setenv("COMPLETION_SAFE", "true")
	if !CompletionSafeEnabled() {
		t.Fatal("COMPLETION_SAFE=true must enable")
	}
}

func TestEvaluateEligibilityFallbacksBeforeCommit(t *testing.T) {
	cases := []struct {
		name   string
		in     EligibilityInput
		reason string
	}{
		{"disabled", EligibilityInput{}, ReasonDisabled},
		{"tools", EligibilityInput{Enabled: true, HasSideEffectTools: true}, ReasonSideEffectTools},
		{"audio", EligibilityInput{Enabled: true, HasAudio: true}, ReasonAudioIntent},
		{"video", EligibilityInput{Enabled: true, HasVideo: true}, ReasonVideoIntent},
		{"large_image", EligibilityInput{Enabled: true, HasLargeImage: true}, ReasonLargeImageIntent},
		{"image_bytes", EligibilityInput{Enabled: true, ImageBytes: LargeImageIntentBytes}, ReasonLargeImageIntent},
		{"overflow", EligibilityInput{Enabled: true, SpoolBytes: 8, SpoolLimit: 4}, ReasonSpoolOverflow},
		{"disk_full", EligibilityInput{Enabled: true, DiskFull: true}, ReasonDiskFull},
		{"not_responses", EligibilityInput{Enabled: true, Format: types.RelayFormatOpenAI}, ReasonNotResponses},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := EvaluateEligibility(tc.in)
			if d.Eligible {
				t.Fatalf("expected ineligible: %+v", d)
			}
			if d.Fallback != FallbackRealtime {
				t.Fatalf("fallback=%s", d.Fallback)
			}
			if d.Reason != tc.reason {
				t.Fatalf("reason=%s want %s", d.Reason, tc.reason)
			}
		})
	}
	ok := EvaluateEligibility(EligibilityInput{Enabled: true, Format: types.RelayFormatOpenAIResponses, Path: "/v1/responses"})
	if !ok.Eligible {
		t.Fatalf("pure-text responses should be eligible: %+v", ok)
	}
}

func TestCompletionSafeOverflowFallsBackWithoutLeak(t *testing.T) {
	mode := NewCompletionSafeMode(true, 32)
	mode.Observe(`data: {"type":"response.created","response":{"id":"resp_fail"}}`)
	mode.Observe(strings.Repeat("x", 64))
	if mode.Enabled() {
		t.Fatal("overflow must disable spool")
	}
	if mode.Fallback() != FallbackRealtime || mode.Reason() != ReasonSpoolOverflow {
		t.Fatalf("fallback=%s reason=%s", mode.Fallback(), mode.Reason())
	}
	if len(mode.Replay()) != 0 {
		t.Fatalf("overflow must drop held events, got %v", mode.Replay())
	}
}
