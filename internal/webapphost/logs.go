package webapphost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"agent-platform/internal/config"
	"agent-platform/internal/webapp"
)

type rotationWriter struct {
	mu          sync.Mutex
	dir, stream string
	size, max   int64
	count       int
	file        *os.File
	failed      bool
}

func newRotationWriter(dir, stream string, cfg config.WebAppLogConfig) (*rotationWriter, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	w := &rotationWriter{dir: dir, stream: stream, max: int64(cfg.MaxSizeMB) << 20, count: cfg.MaxFiles}
	f, err := os.OpenFile(w.path(0), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	w.file = f
	w.size = st.Size()
	return w, nil
}
func (w *rotationWriter) path(index int) string {
	return filepath.Join(w.dir, fmt.Sprintf("%s.%d.log", w.stream, index))
}
func (w *rotationWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	for i := w.count - 1; i > 0; i-- {
		if err := os.Rename(w.path(i-1), w.path(i)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	f, err := os.OpenFile(w.path(0), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	return nil
}
func (w *rotationWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := 0
	for len(data) > 0 {
		if w.file == nil {
			return total, os.ErrClosed
		}
		if w.size >= w.max {
			if err := w.rotate(); err != nil {
				w.failed = true
				return total, err
			}
		}
		size := min(int64(len(data)), w.max-w.size)
		n, err := w.file.Write(data[:size])
		total += n
		w.size += int64(n)
		data = data[n:]
		if err != nil {
			w.failed = true
			return total, err
		}
		if n == 0 {
			w.failed = true
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
func (w *rotationWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
func (w *rotationWriter) Failed() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.failed }
func (r *Runner) Logs(id, stream, cursor string) (webapp.LogPage, error) {
	if len(id) != 23 || !strings.HasPrefix(id, "webapp-") || (stream != "stdout" && stream != "stderr") {
		return webapp.LogPage{}, runtimeError("webapp_log_stream_invalid")
	}
	for _, c := range strings.TrimPrefix(id, "webapp-") {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return webapp.LogPage{}, runtimeError("webapp_log_stream_invalid")
		}
	}
	var data []byte
	digest := sha256.New()
	for index := r.config.Logs.MaxFiles - 1; index >= 0; index-- {
		f, err := os.Open(filepath.Join(r.root, id, "logs", fmt.Sprintf("%s.%d.log", stream, index)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return webapp.LogPage{}, runtimeError("webapp_logs_unavailable")
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return webapp.LogPage{}, runtimeError("webapp_logs_unavailable")
		}
		fmt.Fprintf(digest, "%d:%d:%d;", index, st.Size(), st.ModTime().UnixNano())
		offset := max(int64(0), st.Size()-(64<<10))
		_, err = f.Seek(offset, 0)
		if err != nil {
			f.Close()
			return webapp.LogPage{}, runtimeError("webapp_logs_unavailable")
		}
		part, err := io.ReadAll(io.LimitReader(f, 64<<10))
		f.Close()
		if err != nil {
			return webapp.LogPage{}, runtimeError("webapp_logs_unavailable")
		}
		data = append(data, part...)
		if len(data) > 64<<10 {
			data = data[len(data)-(64<<10):]
		}
	}
	next := hex.EncodeToString(digest.Sum(nil))
	if cursor == next {
		return webapp.LogPage{Cursor: next}, nil
	}
	return webapp.LogPage{Cursor: next, Reset: true, Text: strings.ToValidUTF8(string(data), "�")}, nil
}
