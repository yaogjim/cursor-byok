package logsink

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type RotationConfig struct {
	Prefix        string
	Extension     string
	MaxBytes      int64
	MaxFiles      int
	MaxTotalBytes int64
	MaxAge        time.Duration
	Now           func() time.Time
}

type WriteResult struct {
	Path   string
	Offset int64
	Length int64
}

type RotatingFile struct {
	mu       sync.Mutex
	dir      string
	config   RotationConfig
	file     *os.File
	path     string
	size     int64
	sequence uint64
}

func NewRotatingFile(dir string, config RotationConfig) *RotatingFile {
	config.Prefix = firstNonEmpty(strings.TrimSpace(config.Prefix), "event")
	config.Extension = normalizeExtension(config.Extension)
	if config.MaxBytes <= 0 {
		config.MaxBytes = 16 << 20
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &RotatingFile{
		dir:    strings.TrimSpace(dir),
		config: config,
	}
}

func (writer *RotatingFile) Write(payload []byte) (int, error) {
	result, err := writer.Append(payload)
	return int(result.Length), err
}

func (writer *RotatingFile) Append(payload []byte) (WriteResult, error) {
	if writer == nil {
		return WriteResult{}, fmt.Errorf("rotating file is nil")
	}
	if len(payload) == 0 {
		return WriteResult{}, nil
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if strings.TrimSpace(writer.dir) == "" {
		return WriteResult{}, fmt.Errorf("rotating file directory is empty")
	}
	if err := writer.ensureWritableLocked(int64(len(payload))); err != nil {
		return WriteResult{}, err
	}
	offset := writer.size
	written, err := writer.file.Write(payload)
	writer.size += int64(written)
	result := WriteResult{
		Path:   writer.path,
		Offset: offset,
		Length: int64(written),
	}
	if err != nil {
		_ = writer.closeLocked()
		return result, err
	}
	if written != len(payload) {
		_ = writer.closeLocked()
		return result, io.ErrShortWrite
	}
	return result, nil
}

func (writer *RotatingFile) Close() error {
	if writer == nil {
		return nil
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.closeLocked()
}

func (writer *RotatingFile) ensureWritableLocked(incomingBytes int64) error {
	if writer.file != nil && writer.size > 0 && writer.size+incomingBytes > writer.config.MaxBytes {
		if err := writer.closeLocked(); err != nil {
			return err
		}
	}
	if writer.file != nil {
		return nil
	}
	if err := os.MkdirAll(writer.dir, 0o700); err != nil {
		return fmt.Errorf("create rotating log directory: %w", err)
	}
	if err := os.Chmod(writer.dir, 0o700); err != nil {
		return fmt.Errorf("secure rotating log directory: %w", err)
	}
	now := writer.config.Now().UTC()
	var file *os.File
	var path string
	var err error
	for attempt := 0; attempt < 1024; attempt++ {
		writer.sequence++
		name := fmt.Sprintf(
			"%s-%s-%06d%s",
			writer.config.Prefix,
			now.Format("20060102T150405.000000000Z"),
			writer.sequence,
			writer.config.Extension,
		)
		path = filepath.Join(writer.dir, name)
		file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return fmt.Errorf("open rotating log file: %w", err)
		}
	}
	if file == nil {
		return fmt.Errorf("open rotating log file: exhausted filename attempts")
	}
	writer.file = file
	writer.path = path
	writer.size = 0
	if err := writer.cleanupLocked(now, incomingBytes); err != nil {
		_ = writer.closeLocked()
		return err
	}
	return nil
}

func (writer *RotatingFile) closeLocked() error {
	if writer.file == nil {
		return nil
	}
	err := writer.file.Close()
	writer.file = nil
	writer.path = ""
	writer.size = 0
	return err
}

type retainedFile struct {
	path    string
	name    string
	size    int64
	modTime time.Time
}

func (writer *RotatingFile) cleanupLocked(now time.Time, reservedBytes int64) error {
	entries, err := os.ReadDir(writer.dir)
	if err != nil {
		return fmt.Errorf("scan rotating log directory: %w", err)
	}
	files := make([]retainedFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !writer.managesFile(entry.Name()) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		files = append(files, retainedFile{
			path:    filepath.Join(writer.dir, entry.Name()),
			name:    entry.Name(),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(files, func(left int, right int) bool {
		if files[left].modTime.Equal(files[right].modTime) {
			return files[left].name < files[right].name
		}
		return files[left].modTime.Before(files[right].modTime)
	})
	var totalBytes int64
	for _, file := range files {
		totalBytes += file.size
	}
	if reservedBytes > 0 {
		totalBytes += reservedBytes
	}
	remaining := len(files)
	for _, file := range files {
		if file.path == writer.path {
			continue
		}
		expired := writer.config.MaxAge > 0 && now.Sub(file.modTime) > writer.config.MaxAge
		overFiles := writer.config.MaxFiles > 0 && remaining > writer.config.MaxFiles
		overBytes := writer.config.MaxTotalBytes > 0 && totalBytes > writer.config.MaxTotalBytes
		if !expired && !overFiles && !overBytes {
			continue
		}
		if removeErr := os.Remove(file.path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("remove rotated log file %q: %w", file.path, removeErr)
		}
		remaining--
		totalBytes -= file.size
	}
	return nil
}

func (writer *RotatingFile) managesFile(name string) bool {
	return strings.HasPrefix(name, writer.config.Prefix+"-") && strings.HasSuffix(name, writer.config.Extension)
}

func normalizeExtension(value string) string {
	extension := strings.TrimSpace(value)
	if extension == "" {
		return ".log"
	}
	if strings.HasPrefix(extension, ".") {
		return extension
	}
	return "." + extension
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
