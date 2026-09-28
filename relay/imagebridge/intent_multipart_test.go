package imagebridge

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectMultipartEditWithoutModelUsesC2ADefault(t *testing.T) {
	body, contentType := multipartRequest(t, map[string]string{
		"prompt":  "paint it blue",
		"quality": "low",
		"n":       "20",
	}, []byte("fake-png"))

	intent, ok, err := DetectMultipart("/v1/images/edits", bytes.NewReader(body), contentType)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ModeEdit, intent.Mode)
	assert.Equal(t, DefaultModel, intent.Request.Model)
	assert.Equal(t, "paint it blue", intent.Request.Prompt)
	assert.Equal(t, "high", intent.Request.Quality)
	require.NotNil(t, intent.Request.N)
	assert.EqualValues(t, 4, *intent.Request.N)
	assert.Equal(t, 1, intent.ImageCount)
}

func TestDetectMultipartVariationUsesDefaultPrompt(t *testing.T) {
	body, contentType := multipartRequest(t, nil, []byte("fake-png"))

	intent, ok, err := DetectMultipart("/v1/images/variations", bytes.NewReader(body), contentType)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, DefaultVariationPrompt, intent.Request.Prompt)
	assert.Equal(t, "/v1/images/edits", intent.UpstreamPath())
}

func multipartRequest(t *testing.T, fields map[string]string, image []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		require.NoError(t, writer.WriteField(key, value))
	}
	if image != nil {
		part, err := writer.CreateFormFile("image", "source.png")
		require.NoError(t, err)
		_, err = part.Write(image)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return body.Bytes(), writer.FormDataContentType()
}
