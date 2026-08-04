// Package blob abstracts object storage for item attachments. Production uses
// MinIO over the S3 API; tests (and instances without object storage) use the
// in-memory implementation. A nil Store means attachments are disabled and the
// API surfaces that as 503 — bug reports themselves never depend on it.
package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

type Store interface {
	// Bucket names where objects live, for surfacing the MinIO reference to agents.
	Bucket() string
	Put(ctx context.Context, key, contentType string, size int64, r io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// Memory is a map-backed Store for tests and storage-less dev instances.
type Memory struct {
	mu   sync.RWMutex
	objs map[string][]byte
}

func NewMemory() *Memory { return &Memory{objs: map[string][]byte{}} }

func (m *Memory) Bucket() string { return "memory" }

func (m *Memory) Put(_ context.Context, key, _ string, _ int64, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.objs[key] = b
	m.mu.Unlock()
	return nil
}

func (m *Memory) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.RLock()
	b, ok := m.objs[key]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("blob: no object %q", key)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.objs, key)
	m.mu.Unlock()
	return nil
}
