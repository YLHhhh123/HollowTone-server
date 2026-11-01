package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLimiterBlocksAfterBudget(t *testing.T) {
	now := time.Now()
	l := newLimiter(3, time.Minute)
	l.nowFunc = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("ip"); !ok {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	ok, wait := l.allow("ip")
	if ok {
		t.Fatal("fourth attempt should be blocked")
	}
	if wait <= 0 || wait > time.Minute {
		t.Fatalf("unexpected wait %v", wait)
	}

	// A different client keeps its own budget.
	if ok, _ := l.allow("other"); !ok {
		t.Fatal("a distinct client must not be affected")
	}

	// The window rolls over.
	now = now.Add(time.Minute + time.Second)
	if ok, _ := l.allow("ip"); !ok {
		t.Fatal("budget should reset after the window elapses")
	}
}

func TestLimiterResetOnSuccess(t *testing.T) {
	l := newLimiter(2, time.Minute)
	if ok, _ := l.allow("k"); !ok {
		t.Fatal("first attempt should pass")
	}
	l.reset("k")
	if ok, _ := l.allow("k"); !ok {
		t.Fatal("budget should be available again after reset")
	}
}

// TestStatusWriterKeepsSendfilePath guards the streaming fast path: the logging
// wrapper must delegate io.ReaderFrom to the underlying writer, otherwise
// http.ServeContent loses the kernel copy optimisation for audio streams.
func TestStatusWriterKeepsSendfilePath(t *testing.T) {
	inner := &readFromRecorder{header: http.Header{}}
	var w http.ResponseWriter = &statusWriter{ResponseWriter: inner, status: http.StatusOK}

	rf, ok := w.(io.ReaderFrom)
	if !ok {
		t.Fatal("statusWriter must implement io.ReaderFrom")
	}
	if _, err := rf.ReadFrom(strings.NewReader("stream-me")); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if !inner.sawReadFrom {
		t.Fatal("statusWriter did not delegate to the underlying ReadFrom")
	}
}

func TestStatusWriterRecordsStatus(t *testing.T) {
	inner := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: inner, status: http.StatusOK}
	sw.WriteHeader(http.StatusConflict)
	if sw.status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", sw.status)
	}
}

type readFromRecorder struct {
	header      http.Header
	sawReadFrom bool
}

func (r *readFromRecorder) Header() http.Header         { return r.header }
func (r *readFromRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *readFromRecorder) WriteHeader(int)             {}
func (r *readFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	r.sawReadFrom = true
	return io.Copy(io.Discard, src)
}

func TestCORSDisabledByDefault(t *testing.T) {
	h := cors(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS header must not be emitted when no origins are configured")
	}
}

func TestCORSAllowedOrigin(t *testing.T) {
	h := cors([]string{"https://app.example.com"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	h := cors([]string{"https://app.example.com"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS header must not be emitted for an unlisted origin")
	}
}

func TestClientIPIgnoresForwardedHeaderWithoutTrust(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	if got := clientIP(req, false); got != "192.0.2.10" {
		t.Fatalf("clientIP = %q, want the socket address", got)
	}
	if got := clientIP(req, true); got != "203.0.113.99" {
		t.Fatalf("clientIP with trust = %q, want the forwarded address", got)
	}
}

func TestClientIPTakesFirstForwardedHop(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.7")
	if got := clientIP(req, true); got != "203.0.113.99" {
		t.Fatalf("clientIP = %q, want the first hop", got)
	}
}
