package imagebridge

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

// NewEditBody creates the C2A multipart request as a pipe. Input image bytes
// are read from request-owned storage only when net/http consumes the body; no
// second full request or decoded image buffer is retained in memory.
func NewEditBody(ctx context.Context, intent Intent, storage common.BodyStorage) (io.ReadCloser, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if storage == nil {
		return nil, "", fmt.Errorf("image bridge request storage is missing")
	}
	if intent.Mode != ModeEdit {
		return nil, "", fmt.Errorf("image bridge multipart body requires edit mode")
	}
	if intent.Source == SourceJSON && len(intent.InputImages) == 0 {
		return nil, "", fmt.Errorf("image file or image_url is required")
	}
	if intent.Source == SourceMultipart && strings.TrimSpace(intent.ContentType) == "" {
		return nil, "", fmt.Errorf("multipart content type is missing")
	}

	pipeReader, pipeWriter := io.Pipe()
	multipartWriter := multipart.NewWriter(pipeWriter)
	contentType := multipartWriter.FormDataContentType()
	done := make(chan struct{})

	go func() {
		defer close(done)
		err := writeEditMultipart(ctx, multipartWriter, intent, storage)
		if closeErr := multipartWriter.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = ctx.Err()
		}
		_ = pipeWriter.CloseWithError(err)
	}()

	// A pipe write can be blocked inside net/http. Closing the read side on
	// cancellation wakes the producer immediately and prevents a goroutine or
	// request-storage lease from surviving the downstream request.
	go func() {
		select {
		case <-ctx.Done():
			_ = pipeWriter.CloseWithError(ctx.Err())
		case <-done:
		}
	}()

	return pipeReader, contentType, nil
}

func writeEditMultipart(ctx context.Context, writer *multipart.Writer, intent Intent, storage common.BodyStorage) error {
	if err := writeNormalizedImageFields(ctx, writer, intent); err != nil {
		return err
	}
	switch intent.Source {
	case SourceJSON:
		return writeJSONImages(ctx, writer, intent.InputImages, storage)
	case SourceMultipart:
		return writeMultipartImages(ctx, writer, intent.ContentType, storage)
	default:
		return fmt.Errorf("unsupported image bridge source %q", intent.Source)
	}
}

func writeNormalizedImageFields(ctx context.Context, writer *multipart.Writer, intent Intent) error {
	request := intent.Request
	n := uint(1)
	if request.N != nil {
		n = *request.N
	}
	fields := []struct {
		name  string
		value string
	}{
		{"prompt", request.Prompt},
		{"model", request.Model},
		{"n", strconv.FormatUint(uint64(n), 10)},
		{"size", request.Size},
		{"quality", request.Quality},
		{"response_format", request.ResponseFormat},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writer.WriteField(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

func writeJSONImages(ctx context.Context, writer *multipart.Writer, images []InputImage, storage common.BodyStorage) error {
	for _, image := range images {
		if err := ctx.Err(); err != nil {
			return err
		}
		if image.kind == imageSourceURL {
			if err := writer.WriteField("image", image.URL); err != nil {
				return err
			}
			continue
		}

		source, err := openInputImage(storage, image)
		if err != nil {
			return err
		}
		part, err := createImagePart(writer, "image", image.Filename, image.MIME)
		if err != nil {
			_ = source.Close()
			return err
		}
		_, copyErr := copyWithContext(ctx, part, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func writeMultipartImages(ctx context.Context, writer *multipart.Writer, contentType string, storage common.BodyStorage) error {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("invalid multipart content type: %w", err)
	}
	if !strings.EqualFold(mediaType, "multipart/form-data") || params["boundary"] == "" {
		return fmt.Errorf("invalid multipart content type")
	}
	reader, err := storage.NewReader()
	if err != nil {
		return err
	}
	defer reader.Close()
	multipartReader := multipart.NewReader(reader, params["boundary"])
	imageCount := 0
	maskCount := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		part, nextErr := multipartReader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read source multipart: %w", nextErr)
		}
		name := strings.ToLower(strings.TrimSpace(part.FormName()))
		isMask := name == "mask"
		isImage := isMultipartImageField(name) || part.FileName() != ""
		if !isImage || (isMask && maskCount >= 1) || (!isMask && imageCount >= 4) {
			_, discardErr := copyWithContext(ctx, io.Discard, part)
			_ = part.Close()
			if discardErr != nil {
				return discardErr
			}
			continue
		}
		if isMask {
			maskCount++
		} else {
			imageCount++
		}
		fieldName := "image"
		if isMask {
			fieldName = "mask"
		}
		if part.FileName() == "" {
			value, readErr := io.ReadAll(io.LimitReader(part, maxMultipartFieldBytes+1))
			_ = part.Close()
			if readErr != nil {
				return readErr
			}
			if len(value) > maxMultipartFieldBytes {
				return fmt.Errorf("multipart image URL is too large")
			}
			if err := writer.WriteField(fieldName, strings.TrimSpace(string(value))); err != nil {
				return err
			}
			continue
		}
		mimeType := part.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			mimeType = "image/png"
		}
		destination, createErr := createImagePart(writer, fieldName, part.FileName(), mimeType)
		if createErr != nil {
			_ = part.Close()
			return createErr
		}
		_, copyErr := copyWithContext(ctx, destination, part)
		_ = part.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	if imageCount == 0 {
		return fmt.Errorf("image file or image_url is required")
	}
	return nil
}

func createImagePart(writer *multipart.Writer, fieldName, filename, mimeType string) (io.Writer, error) {
	if filename == "" {
		filename = chooseImageFilename("", mimeType)
	}
	disposition := mime.FormatMediaType("form-data", map[string]string{
		"name":     fieldName,
		"filename": filename,
	})
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", disposition)
	header.Set("Content-Type", mimeType)
	return writer.CreatePart(header)
}

func openInputImage(storage common.BodyStorage, image InputImage) (io.ReadCloser, error) {
	raw, err := openJSONString(storage, image.rawOffset, image.rawLength)
	if err != nil {
		return nil, err
	}
	buffered := bufio.NewReaderSize(raw, 32<<10)
	switch image.kind {
	case imageSourceDataURL:
		header, headerErr := buffered.ReadString(',')
		if headerErr != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("invalid image data URL: %w", headerErr)
		}
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(header)), "data:image/") ||
			!strings.Contains(strings.ToLower(header), ";base64,") {
			_ = raw.Close()
			return nil, fmt.Errorf("invalid image data URL")
		}
		return &readerWithCloser{Reader: base64.NewDecoder(base64.StdEncoding, buffered), Closer: raw}, nil
	case imageSourceRawBase64:
		return &readerWithCloser{Reader: base64.NewDecoder(base64.StdEncoding, buffered), Closer: raw}, nil
	default:
		_ = raw.Close()
		return nil, fmt.Errorf("unsupported inline image source")
	}
}

func openJSONString(storage common.BodyStorage, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 || length <= 0 {
		return nil, fmt.Errorf("invalid JSON image source range")
	}
	reader, err := storage.NewReader()
	if err != nil {
		return nil, err
	}
	if seeker, ok := reader.(io.Seeker); ok {
		if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
			_ = reader.Close()
			return nil, err
		}
	} else if _, err := io.CopyN(io.Discard, reader, offset); err != nil {
		_ = reader.Close()
		return nil, err
	}
	raw := io.LimitReader(reader, length)
	return &readerWithCloser{Reader: newJSONStringUnescaper(raw), Closer: reader}, nil
}

type readerWithCloser struct {
	io.Reader
	io.Closer
}

type jsonStringUnescaper struct {
	reader  *bufio.Reader
	pending []byte
	ended   bool
}

func newJSONStringUnescaper(reader io.Reader) io.Reader {
	return &jsonStringUnescaper{reader: bufio.NewReaderSize(reader, 32<<10)}
}

func (r *jsonStringUnescaper) Read(destination []byte) (int, error) {
	written := 0
	for written < len(destination) {
		if len(r.pending) > 0 {
			n := copy(destination[written:], r.pending)
			written += n
			r.pending = r.pending[n:]
			continue
		}
		if r.ended {
			if written > 0 {
				return written, nil
			}
			return 0, io.EOF
		}
		b, err := r.reader.ReadByte()
		if err != nil {
			r.ended = true
			if err == io.EOF && written > 0 {
				return written, nil
			}
			return written, err
		}
		if b != '\\' {
			r.pending = []byte{b}
			continue
		}
		escape, err := r.reader.ReadByte()
		if err != nil {
			return written, fmt.Errorf("truncated JSON escape: %w", err)
		}
		switch escape {
		case '"', '\\', '/':
			r.pending = []byte{escape}
		case 'b':
			r.pending = []byte{'\b'}
		case 'f':
			r.pending = []byte{'\f'}
		case 'n':
			r.pending = []byte{'\n'}
		case 'r':
			r.pending = []byte{'\r'}
		case 't':
			r.pending = []byte{'\t'}
		case 'u':
			runeValue, decodeErr := r.readUnicodeEscape()
			if decodeErr != nil {
				return written, decodeErr
			}
			var encoded [utf8.UTFMax]byte
			n := utf8.EncodeRune(encoded[:], runeValue)
			r.pending = append(r.pending[:0], encoded[:n]...)
		default:
			return written, fmt.Errorf("invalid JSON escape \\%c", escape)
		}
	}
	return written, nil
}

func (r *jsonStringUnescaper) readUnicodeEscape() (rune, error) {
	first, err := readHexRune(r.reader)
	if err != nil {
		return 0, err
	}
	if first < 0xD800 || first > 0xDBFF {
		return rune(first), nil
	}
	next, err := r.reader.Peek(6)
	if err != nil || len(next) != 6 || next[0] != '\\' || next[1] != 'u' {
		return utf8.RuneError, nil
	}
	secondValue, parseErr := strconv.ParseUint(string(next[2:]), 16, 16)
	if parseErr != nil || secondValue < 0xDC00 || secondValue > 0xDFFF {
		return utf8.RuneError, nil
	}
	_, _ = r.reader.Discard(6)
	return utf16.DecodeRune(rune(first), rune(secondValue)), nil
}

func readHexRune(reader *bufio.Reader) (uint16, error) {
	raw := make([]byte, 4)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return 0, fmt.Errorf("truncated unicode escape: %w", err)
	}
	value, err := strconv.ParseUint(string(raw), 16, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid unicode escape: %w", err)
	}
	return uint16(value), nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != count {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
