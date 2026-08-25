package imagebridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectJSONReaderDoesNotRetainLargeImageString(t *testing.T) {
	rawImage := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x5a}, 4<<20)...)
	encoded := base64.StdEncoding.EncodeToString(rawImage)
	payload := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"edit it"},{"type":"input_image","image_url":"data:image/png;base64,` + encoded + `"}]}],"tools":[{"type":"image_generation"}]}`)

	intent, ok, err := DetectJSONReader("/v1/responses", bytes.NewReader(payload))

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ModeEdit, intent.Mode)
	require.Len(t, intent.InputImages, 1)
	assert.Greater(t, intent.InputImages[0].rawLength, int64(len(encoded)))
	assert.Less(t, len(intent.InputImages[0].preview), 1<<16)
}

func TestNewEditBodyStreamsJSONDataURLIntoMultipart(t *testing.T) {
	rawImage := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x41}, 256<<10)...)
	encoded := base64.StdEncoding.EncodeToString(rawImage)
	payload := []byte(`{"prompt":"turn it blue","image":"data:image/png;base64,` + encoded + `","n":"9","quality":"low"}`)
	storage, err := common.CreateBodyStorage(payload)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })

	intent, ok, err := DetectJSONReader("/v1/images/edits", storage)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, rewind(storage))

	body, contentType, err := NewEditBody(context.Background(), intent, storage)
	require.NoError(t, err)
	t.Cleanup(func() { _ = body.Close() })

	mediaType, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	assert.Equal(t, "multipart/form-data", mediaType)
	reader := multipart.NewReader(body, params["boundary"])
	fields := map[string]string{}
	var gotImage []byte
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		require.NoError(t, nextErr)
		data, readErr := io.ReadAll(part)
		require.NoError(t, readErr)
		if part.FileName() != "" {
			gotImage = data
		} else {
			fields[part.FormName()] = string(data)
		}
	}

	assert.Equal(t, rawImage, gotImage)
	assert.Equal(t, "turn it blue", fields["prompt"])
	assert.Equal(t, DefaultModel, fields["model"])
	assert.Equal(t, "4", fields["n"])
	assert.Equal(t, "high", fields["quality"])
}

func TestNewEditBodyStopsWhenContextIsCanceled(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 4<<20))
	payload := []byte(`{"prompt":"edit it","image":"data:image/png;base64,` + encoded + `"}`)
	storage, err := common.CreateBodyStorage(payload)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	intent, ok, err := DetectJSONReader("/v1/images/edits", storage)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, rewind(storage))

	ctx, cancel := context.WithCancel(context.Background())
	body, _, err := NewEditBody(ctx, intent, storage)
	require.NoError(t, err)
	cancel()

	done := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(io.Discard, body)
		done <- copyErr
	}()
	select {
	case copyErr := <-done:
		require.Error(t, copyErr)
		assert.True(t, errors.Is(copyErr, context.Canceled), copyErr)
	case <-time.After(time.Second):
		t.Fatal("edit body producer did not stop after cancellation")
	}
}

func TestNewEditBodyRejectsInvalidBase64WhileStreaming(t *testing.T) {
	payload := []byte(`{"prompt":"edit it","image":"data:image/png;base64,` + strings.Repeat("!", 256) + `"}`)
	storage, err := common.CreateBodyStorage(payload)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	intent, ok, err := DetectJSONReader("/v1/images/edits", storage)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, rewind(storage))

	body, _, err := NewEditBody(context.Background(), intent, storage)
	require.NoError(t, err)
	t.Cleanup(func() { _ = body.Close() })

	_, err = io.Copy(io.Discard, body)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "base64")
}

func TestWriteEnvelopeBuildsStreamingChatCompletion(t *testing.T) {
	upstream := []byte(`{"data":[{"b64_json":"YWJj"}]}`)
	intent := Intent{Envelope: EnvelopeChat, ClientModel: "gpt-5.6-sol", Stream: true}
	var output bytes.Buffer

	err := WriteEnvelope(&output, intent, upstream, IDs{Response: "resp_test", Item: "ig_test", Created: 123})

	require.NoError(t, err)
	assert.Contains(t, output.String(), `"object":"chat.completion.chunk"`)
	assert.Contains(t, output.String(), `"url":"data:image/png;base64,YWJj"`)
	assert.True(t, strings.HasSuffix(output.String(), "data: [DONE]\n\n"))
}

func TestNativeBridgeFakeC2AEndToEnd(t *testing.T) {
	rawImage := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x27}, 1<<20)...)
	encoded := base64.StdEncoding.EncodeToString(rawImage)
	requestPayload := []byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":[{"type":"input_text","text":"make it green"},{"type":"input_image","image_url":"data:image/png;base64,` + encoded + `"}]}],"tools":[{"type":"image_generation"}]}`)

	upstreamSawImage := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/v1/images/edits", request.URL.Path)
		require.NoError(t, request.ParseMultipartForm(64<<20))
		require.Equal(t, "make it green", request.FormValue("prompt"))
		files := request.MultipartForm.File["image"]
		require.Len(t, files, 1)
		file, err := files[0].Open()
		require.NoError(t, err)
		got, err := io.ReadAll(file)
		_ = file.Close()
		require.NoError(t, err)
		require.Equal(t, rawImage, got)
		upstreamSawImage <- struct{}{}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"data":[{"b64_json":"R0lGODlh"}],"usage":{"total_tokens":11}}`)
	}))
	defer upstream.Close()

	requestStorage, err := common.CreateBodyStorage(requestPayload)
	require.NoError(t, err)
	defer requestStorage.Close()
	intent, matched, err := DetectJSONReader("/v1/responses", requestStorage)
	require.NoError(t, err)
	require.True(t, matched)
	require.NoError(t, rewind(requestStorage))
	body, contentType, err := NewEditBody(context.Background(), intent, requestStorage)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, upstream.URL+intent.UpstreamPath(), body)
	require.NoError(t, err)
	request.Header.Set("Content-Type", contentType)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	select {
	case <-upstreamSawImage:
	case <-time.After(time.Second):
		t.Fatal("fake C2A did not receive the streamed image")
	}

	responseStorage, err := common.CreateBodyStorageFromReader(response.Body, response.ContentLength, 8<<20)
	require.NoError(t, err)
	defer responseStorage.Close()
	responseReader, err := responseStorage.NewReader()
	require.NoError(t, err)
	view, err := ParseImageResponse(responseReader)
	_ = responseReader.Close()
	require.NoError(t, err)
	var clientResponse bytes.Buffer
	err = WriteEnvelopeStorage(&clientResponse, intent, responseStorage, IDs{Response: "resp_test", Item: "ig_test", Created: 123}, view)

	require.NoError(t, err)
	require.JSONEq(t, `{"id":"resp_test","object":"response","created_at":123,"status":"completed","model":"gpt-5.6-sol","output":[{"id":"ig_test","type":"image_generation_call","status":"completed","result":"R0lGODlh"}],"error":null,"usage":{"total_tokens":11}}`, clientResponse.String())
}

func TestFortyEightConcurrentNativeBridgeTransfersKeepHeapBounded(t *testing.T) {
	const (
		workers        = 48
		base64Bytes    = int64(2 << 20)
		maxPayloadSize = int64(4 << 20)
	)
	previousConfig := common.GetDiskCacheConfig()
	common.SetDiskCacheConfig(common.DiskCacheConfig{
		Enabled:     true,
		ThresholdMB: 1,
		MaxSizeMB:   512,
		Path:        t.TempDir(),
	})
	t.Cleanup(func() { common.SetDiskCacheConfig(previousConfig) })

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var peakHeap atomic.Uint64
	peakHeap.Store(before.HeapAlloc)
	stopSampling := make(chan struct{})
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		ticker := time.NewTicker(500 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var current runtime.MemStats
				runtime.ReadMemStats(&current)
				for {
					peak := peakHeap.Load()
					if current.HeapAlloc <= peak || peakHeap.CompareAndSwap(peak, current.HeapAlloc) {
						break
					}
				}
			case <-stopSampling:
				return
			}
		}
	}()

	start := make(chan struct{})
	errorsByWorker := make(chan error, workers)
	var workersDone sync.WaitGroup
	for range workers {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			<-start

			requestStorage, err := common.CreateBodyStorageFromReader(
				newRepeatedJSONReader(
					`{"prompt":"edit it","image":"data:image/png;base64,`,
					base64Bytes,
					`"}`,
				),
				-1,
				maxPayloadSize,
			)
			if err != nil {
				errorsByWorker <- err
				return
			}
			defer requestStorage.Close()
			intent, matched, err := DetectJSONReader("/v1/images/edits", requestStorage)
			if err != nil || !matched {
				if err == nil {
					err = errors.New("image request did not match the native bridge")
				}
				errorsByWorker <- err
				return
			}
			body, _, err := NewEditBody(context.Background(), intent, requestStorage)
			if err != nil {
				errorsByWorker <- err
				return
			}
			_, copyErr := io.Copy(io.Discard, body)
			closeErr := body.Close()
			if copyErr != nil {
				errorsByWorker <- copyErr
				return
			}
			if closeErr != nil {
				errorsByWorker <- closeErr
				return
			}

			responseStorage, err := common.CreateBodyStorageFromReader(
				newRepeatedJSONReader(`{"data":[{"b64_json":"`, base64Bytes, `"}],"usage":{"total_tokens":1}}`),
				-1,
				maxPayloadSize,
			)
			if err != nil {
				errorsByWorker <- err
				return
			}
			defer responseStorage.Close()
			responseReader, err := responseStorage.NewReader()
			if err != nil {
				errorsByWorker <- err
				return
			}
			view, parseErr := ParseImageResponse(responseReader)
			closeErr = responseReader.Close()
			if parseErr != nil {
				errorsByWorker <- parseErr
				return
			}
			if closeErr != nil {
				errorsByWorker <- closeErr
				return
			}
			if err := WriteEnvelopeStorage(io.Discard, Intent{Envelope: EnvelopeResponses}, responseStorage, IDs{Response: "resp_test", Item: "ig_test", Created: 123}, view); err != nil {
				errorsByWorker <- err
				return
			}
			errorsByWorker <- nil
		}()
	}
	close(start)
	workersDone.Wait()
	close(errorsByWorker)
	close(stopSampling)
	<-samplerDone
	for workerErr := range errorsByWorker {
		require.NoError(t, workerErr)
	}

	peakGrowth := peakHeap.Load() - before.HeapAlloc
	t.Logf("peak heap growth for forty-eight concurrent native image transfers: %d bytes", peakGrowth)
	require.Less(t, peakGrowth, uint64(192<<20), "the heap must not retain forty-eight complete request and response bodies")
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	require.LessOrEqual(t, after.HeapAlloc, before.HeapAlloc+(32<<20), "heap must return near baseline after all image transfers close")
}

type repeatedByteReader struct {
	remaining int64
	value     byte
}

func (r *repeatedByteReader) Read(destination []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	count := len(destination)
	if int64(count) > r.remaining {
		count = int(r.remaining)
	}
	for index := range count {
		destination[index] = r.value
	}
	r.remaining -= int64(count)
	return count, nil
}

func newRepeatedJSONReader(prefix string, repeatedBytes int64, suffix string) io.Reader {
	return io.MultiReader(
		strings.NewReader(prefix),
		&repeatedByteReader{remaining: repeatedBytes, value: 'A'},
		strings.NewReader(suffix),
	)
}

func rewind(seeker io.Seeker) error {
	_, err := seeker.Seek(0, io.SeekStart)
	return err
}
