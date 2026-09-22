package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxRequestBody = 8 << 20 // 8 MiB; image payloads arrive as base64

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (a *App) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) withAuthAndLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		if !isPublic(r.URL.Path) && !authorize(r, a.Cfg.APIKeys) {
			writeJSON(sw, http.StatusUnauthorized, errBody("invalid api key"))
			a.logRequest(r, sw.status, time.Since(start))
			return
		}
		next.ServeHTTP(sw, r)
		a.logRequest(r, sw.status, time.Since(start))
	})
}

func isPublic(path string) bool {
	return path == "/healthz" || path == "/"
}

func authorize(r *http.Request, keys []string) bool {
	if len(keys) == 0 {
		return true
	}
	got := bearer(r)
	if got == "" {
		got = r.Header.Get("x-api-key")
	}
	for _, k := range keys {
		if subtle.ConstantTimeCompare([]byte(got), []byte(k)) == 1 {
			return true
		}
	}
	return false
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return h[len("Bearer "):]
	}
	return ""
}

func (a *App) logRequest(r *http.Request, status int, d time.Duration) {
	if !a.Cfg.LogRequests {
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	os.Stdout.WriteString(time.Now().Format("15:04:05") + " " + ip + " " + r.Method + " " + r.URL.Path +
		" -> " + itoa(status) + " (" + itoa(int(d.Milliseconds())) + "ms)\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
