package api

import (
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// --- access logging --------------------------------------------------------

// statusWriter records the response status while passing streaming hints
// through to the underlying writer. Implementing io.ReaderFrom and
// http.Flusher matters: without them http.ServeContent falls back to a generic
// copy loop instead of the kernel's sendfile path, which measurably slows
// audio streaming.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

// ReadFrom preserves the sendfile fast path for http.ServeContent.
func (w *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.written {
		w.written = true
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(w.ResponseWriter, r)
}

// Flush exposes the underlying flusher so streamed responses can be pushed.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the original writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, sw.status, time.Since(start))
	})
}

// --- CORS ------------------------------------------------------------------

// cors answers pre-flight requests and adds the CORS headers required by the
// Flutter web build. It is a no-op when no origins are configured.
func cors(allowed []string, next http.Handler) http.Handler {
	if len(allowed) == 0 {
		return next
	}
	allowAll := false
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
			continue
		}
		if o != "" {
			set[o] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowAll || set[origin]) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Range")
			h.Set("Access-Control-Expose-Headers", "Content-Range, Accept-Ranges, ETag, Content-Length")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- client address --------------------------------------------------------

// clientIP resolves the caller address, honouring X-Forwarded-For only when the
// operator declared the deployment to sit behind a trusted proxy.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				xff = xff[:i]
			}
			if ip := strings.TrimSpace(xff); ip != "" {
				return ip
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- login throttling ------------------------------------------------------

// limiter is a small fixed-window counter keyed by client identity. It exists to
// blunt online password guessing against /auth/login and /auth/register without
// pulling in an external dependency.
type limiter struct {
	mu      sync.Mutex
	hits    map[string]*window
	limit   int
	period  time.Duration
	lastGC  time.Time
	nowFunc func() time.Time
}

type window struct {
	count int
	start time.Time
}

func newLimiter(limit int, period time.Duration) *limiter {
	return &limiter{
		hits:    make(map[string]*window),
		limit:   limit,
		period:  period,
		nowFunc: time.Now,
	}
}

// allow records one attempt for key and reports whether it is still within
// budget, plus how long the caller should wait when it is not.
func (l *limiter) allow(key string) (bool, time.Duration) {
	if l == nil || l.limit <= 0 {
		return true, 0
	}
	now := l.nowFunc()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Opportunistic sweep keeps the map from growing without bound.
	if now.Sub(l.lastGC) > l.period {
		for k, w := range l.hits {
			if now.Sub(w.start) > l.period {
				delete(l.hits, k)
			}
		}
		l.lastGC = now
	}

	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) > l.period {
		l.hits[key] = &window{count: 1, start: now}
		return true, 0
	}
	if w.count >= l.limit {
		return false, l.period - now.Sub(w.start)
	}
	w.count++
	return true, 0
}

// reset clears the budget for key, called after a successful authentication so
// a legitimate user is not penalised by earlier typos.
func (l *limiter) reset(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}
