package channel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func TestNativeAcceleratorTLSBridgeE2E(t *testing.T) {
	if _, err := os.Stat("/work/native-request-adapter-bin"); err != nil {
		t.Skip("skipping TestNativeAcceleratorTLSBridgeE2E: /work/native-request-adapter-bin not present")
	}
	service.InitHttpClient()
	cpa1Hits := 0
	cpa2Hits := 0

	cpa1Srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cpa1Hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg_real1\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_real1\",\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[{\"id\":\"msg_real1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK_FROM_CPA1_TLS\"}]}]}}\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}))
	defer cpa1Srv.Close()

	cpa2Srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cpa2Hits++
		bodyBytes, _ := io.ReadAll(r.Body)
		bodyStr := string(bodyBytes)
		if !strings.Contains(bodyStr, `"model":"gemini-4-argon-high"`) {
			http.Error(w, "expected gemini-4-argon-high model on cpa2", 400)
			return
		}
		if !strings.Contains(bodyStr, `[System Identity Configuration]`) {
			http.Error(w, "expected built-in identity prompt on cpa2", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg_resp_abc_0\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"output_index\":0,\"item_id\":\"msg_resp_abc_0\",\"delta\":\"I am Gemini 4 Argon trained by Google DeepMind.\"}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"id\":\"resp_short\",\"status\":\"completed\",\"model\":\"gemini-4-argon-high\",\"instructions\":\"[System Identity Configuration]\\nmodel_id: gpt-5.6-sol\",\"output\":[{\"id\":\"msg_resp_abc_0\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"summary\":null,\"content\":[{\"type\":\"output_text\",\"text\":\"I am Gemini 4 Argon trained by Google DeepMind.\"}]}]}}\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}))
	defer cpa2Srv.Close()

	// Trust httptest TLS cert in default client
	tr := cpa1Srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig.InsecureSkipVerify = true
	service.GetHttpClient().Transport = tr

	cmd := exec.Command(
		"/work/native-request-adapter-bin",
		"--role=accelerator",
		"--listen=127.0.0.1:28319",
		"--temp-dir=/tmp",
		"--cpa2-upstream="+cpa2Srv.URL,
		"--cpa2-api-key=Aa123456",
	)
	outBuf := &strings.Builder{}
	cmd.Stdout = outBuf
	cmd.Stderr = outBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start accel: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	time.Sleep(400 * time.Millisecond)

	os.Setenv("NATIVE_ACCELERATOR_URL", "http://127.0.0.1:28319")
	os.Setenv("NATIVE_ACCELERATOR_CPA2_URL", cpa2Srv.URL)
	os.Setenv("NATIVE_ACCELERATOR_CPA2_AUTH", "Bearer Aa123456")

	// 1. Non-sol/terra model (gpt-5.4) must hit cpa1Srv and NOT cpa2Srv
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","stream":true,"input":"hi"}`))
		c.Request.Header.Set("X-Accelerator-Test-Ttft-Ms", "150")

		req, err := http.NewRequestWithContext(c.Request.Context(), "POST", cpa1Srv.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5.4","stream":true,"input":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		info := &common.RelayInfo{IsStream: true, OriginModelName: "gpt-5.4"}
		resp, err := doRequest(c, req, info)
		if err != nil {
			t.Fatalf("doRequest gpt-5.4 err: %v\naccel logs:\n%s", err, outBuf.String())
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !strings.Contains(string(body), "OK_FROM_CPA1_TLS") {
			t.Fatalf("expected OK_FROM_CPA1_TLS in body, got:\n%s", string(body))
		}
		if cpa1Hits != 1 || cpa2Hits != 0 {
			t.Fatalf("expected cpa1Hits=1 cpa2Hits=0, got %d / %d", cpa1Hits, cpa2Hits)
		}
	}

	// 2. sol/terra model (gpt-5.6-sol) must automatically reroute through TLS bridge to cpa2Srv (gemini-4-argon-high) and scrub all identity leaks
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","stream":true,"input":"who are you"}`))
		c.Request.Header.Set("X-Accelerator-Test-Ttft-Ms", "150")

		req, err := http.NewRequestWithContext(c.Request.Context(), "POST", cpa1Srv.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","stream":true,"input":"who are you"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		info := &common.RelayInfo{IsStream: true, OriginModelName: "gpt-5.6-sol"}
		resp, err := doRequest(c, req, info)
		if err != nil {
			t.Fatalf("doRequest gpt-5.6-sol err: %v\naccel logs:\n%s", err, outBuf.String())
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		bodyStr := string(body)
		lower := strings.ToLower(bodyStr)
		if cpa2Hits != 1 {
			t.Fatalf("expected cpa2Hits=1, got %d\nbody:\n%s", cpa2Hits, bodyStr)
		}
		for _, forbidden := range []string{"gemini", "argon", "google", "deepmind", "msg_resp_", "system identity configuration"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("leaked forbidden token %q in body:\n%s", forbidden, bodyStr)
			}
		}
		if !strings.Contains(bodyStr, `"model":"gpt-5.6-sol"`) {
			t.Fatalf("expected model gpt-5.6-sol in body:\n%s", bodyStr)
		}
		if !strings.Contains(bodyStr, `"phase":"final_answer"`) {
			t.Fatalf("expected phase final_answer in body:\n%s", bodyStr)
		}
	}
}
