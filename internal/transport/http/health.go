package http

import (
	"context"
	"net/http"
	"time"
)

type Probe interface {
	Ping(context.Context) error
	Ready() bool
}

func Handler(p Probe) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if e := p.Ping(ctx); e != nil {
			http.Error(w, "unhealthy", 503)
			return
		}
		w.Write([]byte("ok\n"))
	})
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !p.Ready() {
			http.Error(w, "not ready", 503)
			return
		}
		w.Write([]byte("ready\n"))
	})
	return m
}
