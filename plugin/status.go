package sessionguard

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/webneco/bifrost-session-guard/guard"
)

//go:embed dashboard.html
var dashboardHTML []byte

// statusServer serves the guard's status API and dashboard on its own listener, so it works
// on any Bifrost build without touching the gateway's routes or UI.
type statusServer struct {
	addr string
	srv  *http.Server
}

type summary struct {
	Totals guard.Totals `json:"totals"`
	Config guard.Config `json:"config"`
	Now    time.Time    `json:"now"`
}

func startStatusServer(addr string, engine *guard.Engine) (*statusServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	token := engine.Config().Status.Token

	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	authed := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if token != "" {
				got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or wrong bearer token"})
					return
				}
			}
			next(w, r)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/summary", authed(func(w http.ResponseWriter, _ *http.Request) {
		cfg := engine.Config()
		cfg.Status.Token = "" // never echo the token
		writeJSON(w, http.StatusOK, summary{Totals: engine.Totals(), Config: cfg, Now: time.Now()})
	}))
	mux.HandleFunc("GET /api/sessions", authed(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, engine.Sessions())
	}))
	mux.HandleFunc("GET /api/session", authed(func(w http.ResponseWriter, r *http.Request) {
		s, ok := engine.Session(r.URL.Query().Get("key"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
			return
		}
		writeJSON(w, http.StatusOK, s)
	}))
	mux.HandleFunc("POST /api/reset", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.Key == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send {\"key\": \"<session key>\"}"})
			return
		}
		if !engine.Reset(body.Key) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
	}))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(dashboardHTML)
	})

	s := &statusServer{
		addr: ln.Addr().String(),
		srv:  &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second},
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = err // the listener is gone; the guard itself keeps working
		}
	}()
	return s, nil
}

func (s *statusServer) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}
