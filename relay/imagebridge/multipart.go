package imagebridge

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"
)

const maxMultipartFieldBytes = 64 << 10

func DetectMultipart(path string, reader io.Reader, contentType string) (Intent, bool, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return Intent{}, false, fmt.Errorf("invalid multipart content type: %w", err)
	}
	if !strings.EqualFold(mediaType, "multipart/form-data") || params["boundary"] == "" {
		return Intent{}, false, nil
	}

	fields := make(map[string]string)
	imageCount := 0
	multipartReader := multipart.NewReader(reader, params["boundary"])
	for {
		part, err := multipartReader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Intent{}, false, fmt.Errorf("read multipart image request: %w", err)
		}
		name := strings.ToLower(strings.TrimSpace(part.FormName()))
		if part.FileName() != "" || isMultipartImageField(name) {
			if name != "mask" && imageCount < 4 {
				imageCount++
			}
			_, copyErr := io.Copy(io.Discard, part)
			_ = part.Close()
			if copyErr != nil {
				return Intent{}, false, fmt.Errorf("read multipart image: %w", copyErr)
			}
			continue
		}

		value, readErr := io.ReadAll(io.LimitReader(part, maxMultipartFieldBytes+1))
		_ = part.Close()
		if readErr != nil {
			return Intent{}, false, fmt.Errorf("read multipart field %q: %w", name, readErr)
		}
		if len(value) > maxMultipartFieldBytes {
			return Intent{}, false, fmt.Errorf("multipart field %q is too large", name)
		}
		fields[name] = strings.TrimSpace(string(value))
	}

	n := 1
	if raw := fields["n"]; raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			n = parsed
		}
	}
	payload := map[string]any{
		"model":   fields["model"],
		"prompt":  fields["prompt"],
		"size":    fields["size"],
		"quality": fields["quality"],
		"n":       n,
	}
	if raw := fields["stream"]; raw != "" {
		if stream, err := strconv.ParseBool(raw); err == nil {
			payload["stream"] = stream
		}
	}
	if imageCount > 0 {
		images := make([]any, imageCount)
		for index := range images {
			images[index] = "multipart-image"
		}
		payload["images"] = images
	}

	intent, matched, err := detect(path, payload)
	if err != nil || !matched {
		return intent, matched, err
	}
	intent.ImageCount = imageCount
	intent.Source = SourceMultipart
	intent.ContentType = contentType
	if intent.Mode == ModeEdit && imageCount == 0 {
		return Intent{}, false, fmt.Errorf("image file or image_url is required")
	}
	return intent, true, nil
}

func isMultipartImageField(name string) bool {
	if name == "mask" {
		return true
	}
	return name == "image" || name == "images" || name == "image[]" || name == "images[]" ||
		strings.HasPrefix(name, "image[")
}
