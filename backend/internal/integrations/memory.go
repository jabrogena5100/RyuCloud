package integrations

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStorage is an in-process ObjectStorage for tests: it records presign
// requests, lets a test "upload" an object with Put (standing in for the
// browser's direct upload), stores real bytes written with PutObject, and can be
// told to fail. Not durable.
type MemoryStorage struct {
	mu        sync.Mutex
	objects   map[string]memObject
	presigned []PresignRequest
	deleted   []string
	errs      map[string]error
}

type memObject struct {
	info ObjectInfo
	data []byte
}

// Operation names accepted by FailWith.
const (
	OpPresign = "presign"
	OpStat    = "stat"
	OpDelete  = "delete" // DeleteObject and DeletePrefix
	OpGet     = "get"
	OpPut     = "put"
)

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{objects: map[string]memObject{}, errs: map[string]error{}}
}

// Put stores an object as if the browser had uploaded size bytes to its presigned URL.
// The object has a size and type but no readable content.
func (m *MemoryStorage) Put(objectKey, contentType string, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[objectKey] = memObject{info: ObjectInfo{Size: size, ContentType: contentType, ETag: "etag-" + objectKey}}
}

// PutData stores an object with real content, as if uploaded by the browser.
func (m *MemoryStorage) PutData(objectKey, contentType string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[objectKey] = memObject{
		info: ObjectInfo{Size: int64(len(data)), ContentType: contentType, ETag: "etag-" + objectKey},
		data: append([]byte(nil), data...),
	}
}

// Data returns a copy of an object's content.
func (m *MemoryStorage) Data(objectKey string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[objectKey]
	return append([]byte(nil), obj.data...), ok
}

// ContentType returns the stored content type of an object.
func (m *MemoryStorage) ContentType(objectKey string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[objectKey].info.ContentType
}

// FailWith makes every later call of op return err. A nil err restores normal behaviour.
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

// KeysWithPrefix returns the sorted keys starting with prefix.
func (m *MemoryStorage) KeysWithPrefix(prefix string) []string {
	var out []string
	for _, key := range m.Keys() {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	return out
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
	obj, ok := m.objects[objectKey]
	if !ok {
		return ObjectInfo{}, ErrObjectNotFound
	}
	return obj.info, nil
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

func (m *MemoryStorage) GetObject(_ context.Context, objectKey string) (ObjectReader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.errs[OpGet]; err != nil {
		return ObjectReader{}, err
	}
	obj, ok := m.objects[objectKey]
	if !ok {
		return ObjectReader{}, ErrObjectNotFound
	}
	data := obj.data
	if data == nil && obj.info.Size > 0 { // Put() objects have a size but no content
		data = make([]byte, obj.info.Size)
	}
	return ObjectReader{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data)), ContentType: obj.info.ContentType}, nil
}

func (m *MemoryStorage) PutObject(_ context.Context, objectKey, contentType string, body io.ReadSeeker, size int64) error {
	m.mu.Lock()
	err := m.errs[OpPut]
	m.mu.Unlock()
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(body)
	if readErr != nil {
		return readErr
	}
	m.PutData(objectKey, contentType, data)
	return nil
}

func (m *MemoryStorage) DeletePrefix(_ context.Context, prefix string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.errs[OpDelete]; err != nil {
		return 0, err
	}
	n := 0
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			delete(m.objects, key)
			n++
		}
	}
	return n, nil
}
