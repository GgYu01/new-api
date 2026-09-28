package lifecycle

import "testing"

func TestPreserveSessionStateErrorKeepsTypeAndForbidsStripReplay(t *testing.T) {
	code, msg, strip := PreserveSessionStateError("thinking_signature_invalid", "encrypted_content expired")
	if code != "thinking_signature_invalid" {
		t.Fatalf("code=%s", code)
	}
	if msg != SessionStateInvalidMessage {
		t.Fatalf("msg=%s", msg)
	}
	if strip {
		t.Fatal("must not silently strip encrypted state and replay")
	}
}
