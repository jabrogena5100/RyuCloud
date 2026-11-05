package integrations

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStorage is an in-process ObjectStorage for tests: it records presign
// requests, lets a test "upload" an object with Put (standing in for the
// browser's direct upload) and can be told to fail. Not durable.
type MemoryStorage struct {
	mu        sync.Mutex
	objects   map[string]ObjectInfo
	presigned []PresignRequest
	deleted   []string
	errs      map[string]error
}

// Operation names accepted by FailWith.
const (
	OpPresign = "presign"
	OpStat    = "stat"
	OpDelete  = "delete"
)

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{objects: map[string]ObjectInfo{}, errs: map[string]error{}}
}

// Put stores an object as if the browser had uploaded size bytes to its presigned URL.
func (m *MemoryStorage) Put(objectKey, contentType string, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[objectKey] = ObjectInfo{Size: size, ContentType: contentType, ETag: "etag-" + objectKey}
}

// FailWith makes every later call of op (OpPresign, OpStat or OpDelete) return err.
// A nil err restores normal behaviour.
func (m *MemoryStorage) FailWith(op string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.errs, op)
		return
	}
	m.errs[op] = err
}

// Has reports whether an object is currently stored.
func (m *MemoryStorage) Has(objectKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[objectKey]
	return ok
}

// Keys returns the stored object keys, sorted.
func (m *MemoryStorage) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.objects))
	for key := range m.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Presigned returns a copy of every presign request received.
func (m *MemoryStorage) Presigned() []PresignRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]PresignRequest(nil), m.presigned...)
}

// Deleted returns the keys DeleteObject was called with, in order.
func (m *MemoryStorage) Deleted() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.deleted...)
}

func (m *MemoryStorage) PresignUpload(_ context.Context, req PresignRequest) (PresignedUpload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.errs[OpPresign]; err != nil {
		return PresignedUpload{}, err
	}
	m.presigned = append(m.presigned, req)
	return PresignedUpload{
		Method:    "PUT",
		URL:       "http://storage.test/" + req.ObjectKey + "?signature=test",
		Headers:   map[string]string{"Content-Type": req.ContentType},
		ObjectKey: req.ObjectKey,
		ExpiresAt: time.Now().Add(req.TTL),
	}, nil
}

func (m *MemoryStorage) StatObject(_ context.Context, objectKey string) (ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.errs[OpStat]; err != nil {
		return ObjectInfo{}, err
	}
	info, ok := m.objects[objectKey]
	if !ok {
		return ObjectInfo{}, ErrObjectNotFound
	}
	return info, nil
}

func (m *MemoryStorage) DeleteObject(_ context.Context, objectKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.errs[OpDelete]; err != nil {
		return err
	}
	m.deleted = append(m.deleted, objectKey)
	delete(m.objects, objectKey)
	return nil
}
