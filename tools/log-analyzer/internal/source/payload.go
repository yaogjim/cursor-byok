package source

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const DefaultMaxPayloadBytes int64 = 8 << 20

type PayloadRequest struct {
	EventsFilePath string
	Reference      string
	MaxBytes       int64
}

type PayloadDocument struct {
	Content   []byte
	Sensitive bool
}

func ReadPayload(request PayloadRequest) (PayloadDocument, error) {
	eventsPath, err := filepath.Abs(filepath.Clean(strings.TrimSpace(request.EventsFilePath)))
	if err != nil || strings.TrimSpace(request.EventsFilePath) == "" {
		return PayloadDocument{}, errors.New("events file path is required")
	}
	reference := filepath.FromSlash(strings.TrimSpace(request.Reference))
	if reference == "" || filepath.IsAbs(reference) {
		return PayloadDocument{}, errors.New("payload reference must be relative")
	}
	cleanReference := filepath.Clean(reference)
	if cleanReference == "." || cleanReference == ".." || strings.HasPrefix(cleanReference, ".."+string(filepath.Separator)) {
		return PayloadDocument{}, errors.New("payload reference escapes session root")
	}
	parts := strings.Split(filepath.ToSlash(cleanReference), "/")
	if len(parts) < 2 || parts[0] != "payloads" {
		return PayloadDocument{}, errors.New("payload reference must stay under payloads/")
	}
	sessionRoot := filepath.Dir(eventsPath)
	trustedRoot, err := filepath.EvalSymlinks(sessionRoot)
	if err != nil {
		return PayloadDocument{}, fmt.Errorf("resolve session root: %w", err)
	}
	candidate := filepath.Join(trustedRoot, cleanReference)
	info, err := os.Lstat(candidate)
	if err != nil {
		return PayloadDocument{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return PayloadDocument{}, errors.New("payload reference points to a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return PayloadDocument{}, errors.New("payload reference is not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return PayloadDocument{}, err
	}
	if !isWithinRoot(trustedRoot, resolved) {
		return PayloadDocument{}, errors.New("payload reference escapes trusted session root")
	}
	maxBytes := request.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxPayloadBytes
	}
	if info.Size() > maxBytes {
		return PayloadDocument{}, fmt.Errorf("payload exceeds %d bytes", maxBytes)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return PayloadDocument{}, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return PayloadDocument{}, err
	}
	if int64(len(content)) > maxBytes {
		return PayloadDocument{}, fmt.Errorf("payload exceeds %d bytes", maxBytes)
	}
	content = bytes.TrimSpace(content)
	if !utf8.Valid(content) {
		return PayloadDocument{}, errors.New("payload is not valid UTF-8")
	}
	if !json.Valid(content) {
		return PayloadDocument{}, errors.New("payload is not valid JSON")
	}
	return PayloadDocument{Content: append([]byte(nil), content...), Sensitive: true}, nil
}

func isWithinRoot(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
