package logsink

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type PayloadRef struct {
	Path   string            `json:"path,omitempty"`
	Offset int64             `json:"offset,omitempty"`
	Length int64             `json:"length,omitempty"`
	SHA256 string            `json:"sha256"`
	Chunks []PayloadChunkRef `json:"chunks,omitempty"`
}

type PayloadChunkRef struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

type PayloadPackStore struct {
	root   string
	writer *RotatingFile
}

type payloadRecord struct {
	SchemaVersion int               `json:"schema_version"`
	Kind          string            `json:"kind"`
	SHA256        string            `json:"sha256"`
	ChunkSHA256   string            `json:"chunk_sha256,omitempty"`
	ChunkIndex    int               `json:"chunk_index,omitempty"`
	ChunkCount    int               `json:"chunk_count,omitempty"`
	Encoding      string            `json:"encoding"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Data          any               `json:"data"`
}

func NewPayloadPackStore(root string, config RotationConfig) *PayloadPackStore {
	root = strings.TrimSpace(root)
	config.Prefix = firstNonEmpty(config.Prefix, "pack")
	config.Extension = firstNonEmpty(config.Extension, ".jsonl")
	return &PayloadPackStore{
		root:   root,
		writer: NewRotatingFile(filepath.Join(root, "payloads"), config),
	}
}

func (store *PayloadPackStore) Put(kind string, payload []byte, metadata map[string]string) (PayloadRef, error) {
	if store == nil || store.writer == nil || strings.TrimSpace(store.root) == "" {
		return PayloadRef{}, fmt.Errorf("payload pack store is not initialized")
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	chunkSize := payloadChunkSize(store.writer.config.MaxBytes)
	chunkCount := payloadChunkCount(len(payload), chunkSize)
	chunks := make([]PayloadChunkRef, 0, chunkCount)
	for index := 0; index < chunkCount; index++ {
		start := index * chunkSize
		end := start + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[start:end]
		chunkRef, err := store.appendChunk(kind, digest, chunk, index, chunkCount, metadata)
		if err != nil {
			return PayloadRef{}, err
		}
		chunks = append(chunks, chunkRef)
	}
	if len(chunks) == 1 {
		return PayloadRef{
			Path:   chunks[0].Path,
			Offset: chunks[0].Offset,
			Length: chunks[0].Length,
			SHA256: digest,
		}, nil
	}
	return PayloadRef{SHA256: digest, Chunks: chunks}, nil
}

func (store *PayloadPackStore) appendChunk(kind string, payloadDigest string, chunk []byte, index int, count int, metadata map[string]string) (PayloadChunkRef, error) {
	chunkSum := sha256.Sum256(chunk)
	encoding, data := encodePayloadData(chunk)
	record := payloadRecord{
		SchemaVersion: 1,
		Kind:          strings.TrimSpace(kind),
		SHA256:        payloadDigest,
		ChunkSHA256:   hex.EncodeToString(chunkSum[:]),
		ChunkIndex:    index,
		ChunkCount:    count,
		Encoding:      encoding,
		Metadata:      cloneMetadata(metadata),
		Data:          data,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return PayloadChunkRef{}, fmt.Errorf("encode payload pack record: %w", err)
	}
	encoded = append(encoded, '\n')
	result, err := store.writer.Append(encoded)
	if err != nil {
		return PayloadChunkRef{}, err
	}
	relativePath, relErr := filepath.Rel(store.root, result.Path)
	if relErr != nil {
		relativePath = result.Path
	}
	return PayloadChunkRef{
		Path:   filepath.ToSlash(relativePath),
		Offset: result.Offset,
		Length: result.Length,
	}, nil
}

func (store *PayloadPackStore) Close() error {
	if store == nil || store.writer == nil {
		return nil
	}
	return store.writer.Close()
}

func payloadChunkSize(maxBytes int64) int {
	if maxBytes <= 0 {
		return 8 << 20
	}
	size := maxBytes / 2
	if size < 1024 {
		size = 1024
	}
	maxInt := int64(^uint(0) >> 1)
	if size > maxInt {
		size = maxInt
	}
	return int(size)
}

func payloadChunkCount(payloadLength int, chunkSize int) int {
	if payloadLength == 0 || chunkSize <= 0 {
		return 1
	}
	return (payloadLength + chunkSize - 1) / chunkSize
}

func encodePayloadData(payload []byte) (string, any) {
	return "base64", base64.StdEncoding.EncodeToString(payload)
}

func cloneMetadata(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return cloned
}
