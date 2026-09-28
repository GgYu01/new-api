package imagebridge

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteEnvelopeKeepsImagesAPIResponseVerbatim(t *testing.T) {
	upstream := []byte(`{"created":1,"data":[{"b64_json":"YWJj"}]}`)
	var output bytes.Buffer

	err := WriteEnvelope(&output, Intent{Envelope: EnvelopeImages}, upstream, IDs{})

	require.NoError(t, err)
	assert.Equal(t, upstream, output.Bytes())
}

func TestWriteEnvelopeBuildsResponsesImageCallWithoutDecodingBase64(t *testing.T) {
	upstream := []byte(`{"created":1,"data":[{"b64_json":"YWJj","revised_prompt":"clean"}],"usage":{"total_tokens":7}}`)
	intent := Intent{Envelope: EnvelopeResponses, ClientModel: "gpt-5.6-sol"}
	var output bytes.Buffer

	err := WriteEnvelope(&output, intent, upstream, IDs{Response: "resp_test", Item: "ig_test", Created: 123})

	require.NoError(t, err)
	assert.JSONEq(t, `{
		"id":"resp_test","object":"response","created_at":123,"status":"completed",
		"model":"gpt-5.6-sol","output":[{"id":"ig_test","type":"image_generation_call",
		"status":"completed","revised_prompt":"clean","result":"YWJj"}],"error":null,
		"usage":{"total_tokens":7}
	}`, output.String())
}

func TestWriteEnvelopeBuildsChatImageURL(t *testing.T) {
	upstream := []byte(`{"data":[{"b64_json":"YWJj"}]}`)
	intent := Intent{Envelope: EnvelopeChat, ClientModel: "gpt-5.6-sol"}
	var output bytes.Buffer

	err := WriteEnvelope(&output, intent, upstream, IDs{Response: "resp_test", Item: "ig_test", Created: 123})

	require.NoError(t, err)
	assert.JSONEq(t, `{
		"id":"chatcmpl_test","object":"chat.completion","created":123,"model":"gpt-5.6-sol",
		"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"image_url",
		"image_url":{"url":"data:image/png;base64,YWJj"}}]},"finish_reason":"stop"}]
	}`, output.String())
}

func TestWriteEnvelopeBuildsResponsesSSE(t *testing.T) {
	upstream := []byte(`{"data":[{"b64_json":"YWJj"}],"usage":{"total_tokens":7}}`)
	intent := Intent{Envelope: EnvelopeResponses, ClientModel: "gpt-5.6-sol", Stream: true}
	var output bytes.Buffer

	err := WriteEnvelope(&output, intent, upstream, IDs{Response: "resp_test", Item: "ig_test", Created: 123})

	require.NoError(t, err)
	assert.Contains(t, output.String(), "event: response.created\n")
	assert.Contains(t, output.String(), "event: response.image_generation_call.completed\n")
	assert.Contains(t, output.String(), `"result":"YWJj"`)
	assert.Contains(t, output.String(), "data: [DONE]\n\n")
}

func TestWriteEnvelopeRejectsSuccessfulResponseWithoutImageData(t *testing.T) {
	var output bytes.Buffer

	err := WriteEnvelope(&output, Intent{Envelope: EnvelopeResponses}, []byte(`{"data":[]}`), IDs{})

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoImageData))
	assert.Empty(t, output.Bytes())
}

func TestWriteFailureBuildsTerminalResponsesSSE(t *testing.T) {
	var output bytes.Buffer

	err := WriteFailure(&output, Intent{ClientModel: "gpt-5.6-sol", Stream: true}, IDs{Response: "resp_test", Created: 123}, "upstream timeout")

	require.NoError(t, err)
	assert.Contains(t, output.String(), "event: response.failed\n")
	assert.Contains(t, output.String(), `"code":"image_generation_failed"`)
	assert.Contains(t, output.String(), `"message":"upstream timeout"`)
	assert.True(t, strings.HasSuffix(output.String(), "data: [DONE]\n\n"))
}

func TestParseAndWriteStoredEnvelopeDoesNotRetainOrWriteOneHugeBase64String(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 4<<20))
	payload := []byte(`{"created":1,"data":[{"b64_json":"` + encoded + `","revised_prompt":"clean"}],"usage":{"total_tokens":7}}`)
	storage, err := common.CreateBodyStorage(payload)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	reader, err := storage.NewReader()
	require.NoError(t, err)
	view, err := ParseImageResponse(reader)
	_ = reader.Close()

	require.NoError(t, err)
	require.NotNil(t, view.b64)
	assert.True(t, view.b64.truncated)
	assert.Less(t, len(view.b64.value), 1<<16)

	writer := &maxChunkWriter{}
	err = WriteEnvelopeStorage(writer, Intent{Envelope: EnvelopeResponses, ClientModel: "gpt-5.6-sol"}, storage, IDs{Response: "resp_test", Item: "ig_test", Created: 123}, view)

	require.NoError(t, err)
	assert.Greater(t, writer.total, int64(len(encoded)))
	assert.LessOrEqual(t, writer.maxChunk, 32<<10)
}

type maxChunkWriter struct {
	total    int64
	maxChunk int
}

func (w *maxChunkWriter) Write(data []byte) (int, error) {
	if len(data) > w.maxChunk {
		w.maxChunk = len(data)
	}
	w.total += int64(len(data))
	return len(data), nil
}

var _ io.Writer = (*maxChunkWriter)(nil)
