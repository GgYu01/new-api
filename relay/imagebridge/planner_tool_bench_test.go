package imagebridge

import (
	"strings"
	"testing"
)

// benchBody builds a realistic chat-completions body: a conversation with
// several medium messages plus optional tools.
func benchBody(withTools bool, repeat int) []byte {
	msg := `{"role":"user","content":"Please review the following service description and answer in detail: #proc-dispatch waits for the readiness probe before draining the legacy queue, so a rolling restart keeps the socket warm while the shard map is rebalanced. "}`
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5.6-sol","messages":[`)
	b.WriteString(strings.Repeat(msg+",", repeat))
	b.WriteString(`{"role":"user","content":"Summarize in one sentence."}]`)
	if withTools {
		b.WriteString(`,"tools":[{"type":"image_generation","description":"Generate an image"}],"tool_choice":"auto"`)
	}
	b.WriteString(`}`)
	return []byte(b.String())
}

func benchmarkRewrite(b *testing.B, withTools bool, repeat int) {
	body := benchBody(withTools, repeat)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, changed, err := RewriteAutoImageToolForEnvelope(body, EnvelopeChat)
		if err != nil {
			b.Fatal(err)
		}
		if changed != withTools {
			b.Fatalf("changed=%v want %v", changed, withTools)
		}
		if len(out) == 0 {
			b.Fatal("empty output")
		}
	}
}

func BenchmarkRewritePassthroughSmall(b *testing.B) { benchmarkRewrite(b, false, 1) }
func BenchmarkRewritePassthroughLarge(b *testing.B) { benchmarkRewrite(b, false, 24) }
func BenchmarkRewriteRewriteSmall(b *testing.B)     { benchmarkRewrite(b, true, 1) }
func BenchmarkRewriteRewriteLarge(b *testing.B)     { benchmarkRewrite(b, true, 24) }
