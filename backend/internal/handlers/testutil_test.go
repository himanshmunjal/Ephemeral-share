package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"

	"ephemeral-share/internal/config"
	"ephemeral-share/internal/handlers"
	"ephemeral-share/internal/redisclient"
	"ephemeral-share/internal/storage"
)

const testOrigin = "http://app.test"

func init() { gin.SetMode(gin.TestMode) }

// ---- fake S3 --------------------------------------------------------------

type fakeObject struct {
	body []byte
	mod  time.Time
}

// fakeS3 implements just enough of the S3 path-style API (PUT/HEAD/GET-list/
// DELETE) for the storage client, without checking signatures.
type fakeS3 struct {
	mu   sync.Mutex
	objs map[string]fakeObject
	srv  *httptest.Server
}

func newFakeS3(t *testing.T) *fakeS3 {
	f := &fakeS3{objs: map[string]fakeObject{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeS3) keyOf(r *http.Request) string {
	return strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"), "b/")
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.keyOf(r)
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objs[key] = fakeObject{body: body, mod: time.Now()}
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		o, ok := f.objs[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(o.body)))
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		delete(f.objs, key)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		if r.URL.Query().Get("list-type") != "" {
			prefix := r.URL.Query().Get("prefix")
			keys := make([]string, 0)
			for k := range f.objs {
				if strings.HasPrefix(k, prefix) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			var b strings.Builder
			b.WriteString(`<?xml version="1.0"?><ListBucketResult><Name>b</Name><IsTruncated>false</IsTruncated>`)
			for _, k := range keys {
				o := f.objs[k]
				fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>%s</LastModified><Size>%d</Size></Contents>",
					k, o.mod.UTC().Format("2006-01-02T15:04:05.000Z"), len(o.body))
			}
			b.WriteString(`</ListBucketResult>`)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, b.String())
			return
		}
		o, ok := f.objs[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(o.body)
	}
}

func (f *fakeS3) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.objs {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeS3) plant(key string, age time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[key] = fakeObject{body: []byte("x"), mod: time.Now().Add(-age)}
}

// ---- test environment -----------------------------------------------------

type env struct {
	t     *testing.T
	api   *httptest.Server
	mr    *miniredis.Miniredis
	s3    *fakeS3
	cfg   *config.Config
	store *redisclient.Store
	sc    *storage.Client
}

// newEnv boots the real router against an in-memory Redis and a fake S3.
// Small limits keep the quota tests short; pass overrides as KEY=VALUE.
func newEnv(t *testing.T, overrides ...string) *env {
	t.Helper()
	mr := miniredis.RunT(t)
	fs := newFakeS3(t)

	defaults := map[string]string{
		"REDIS_ADDR":            mr.Addr(),
		"S3_ENDPOINT":           fs.srv.URL,
		"S3_PUBLIC_ENDPOINT":    fs.srv.URL,
		"S3_BUCKET":             "b",
		"S3_ACCESS_KEY":         "k",
		"S3_SECRET_KEY":         "s",
		"ALLOWED_ORIGIN":        testOrigin,
		"MAX_UPLOAD_MB":         "1",
		"MAX_SESSION_MB":        "2",
		"MAX_FILES_PER_SESSION": "3",
		"MAX_PARTICIPANTS":      "2",
		"MAX_FAILED_LOOKUPS":    "5",
		"RATE_LIMIT_PER_MIN":    "600",
		"ORPHAN_GRACE_SECONDS":  "1",
	}
	for _, o := range overrides {
		k, v, _ := strings.Cut(o, "=")
		defaults[k] = v
	}
	for k, v := range defaults {
		t.Setenv(k, v)
	}
	cfg := config.Load()

	store := redisclient.New(cfg.RedisAddr, cfg.RedisPassword, cfg.SessionTTL, cfg.RedisTLS)
	sc, err := storage.New(context.Background(), cfg.S3Endpoint, cfg.S3PublicEndpoint, cfg.S3Region, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handlers.NewRouter(store, sc, cfg, nil))
	t.Cleanup(srv.Close)
	return &env{t: t, api: srv, mr: mr, s3: fs, cfg: cfg, store: store, sc: sc}
}

type resp struct {
	Status int
	Body   map[string]any
	Header http.Header
}

func (e *env) do(method, path string, body any, headers ...string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.api.URL+path, rd)
	req.Header.Set("Origin", testOrigin)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Header: res.Header}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

// newSession creates a session and returns (code, ownerToken).
func (e *env) newSession(sessionType string) (string, string) {
	e.t.Helper()
	r := e.do("POST", "/api/v1/sessions", map[string]any{"type": sessionType})
	if r.Status != http.StatusCreated {
		e.t.Fatalf("create session: %d %v", r.Status, r.Body)
	}
	return r.Body["code"].(string), r.Body["owner_token"].(string)
}

const (
	alice = "participant-alice-0001"
	bob   = "participant-bob-00002"
)

// upload runs the full presign → PUT → complete flow and returns the item id.
func (e *env) upload(code, who string, size int) (string, resp) {
	e.t.Helper()
	r := e.requestUpload(code, who, size)
	if r.Status != http.StatusOK {
		return "", r
	}
	e.putBytes(r.Body["upload_url"].(string), size)
	id := r.Body["content_id"].(string)
	return id, e.do("POST", fmt.Sprintf("/api/v1/sessions/%s/content/%s/complete", code, id), nil, "X-Participant-ID", who)
}

func (e *env) requestUpload(code, who string, size int) resp {
	return e.do("POST", fmt.Sprintf("/api/v1/sessions/%s/content/upload-url", code),
		map[string]any{"filename": "enc-name", "content_type": "application/octet-stream", "size_bytes": size},
		"X-Participant-ID", who)
}

func (e *env) putBytes(url string, n int) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(make([]byte, n)))
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		e.t.Fatalf("PUT to storage failed: %v %v", err, res)
	}
	res.Body.Close()
}
