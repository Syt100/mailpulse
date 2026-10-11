package http

import (
	"context"
	"net/http/httptest"
	"testing"
)

type probe struct{ ready bool }

func (probe) Ping(context.Context) error { return nil }
func (p probe) Ready() bool              { return p.ready }
func TestHealthAllowsAccountAuthFailure(t *testing.T) {
	h := Handler(probe{false})
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/healthz", nil))
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
}
func TestReadyReportsStoppedSender(t *testing.T) {
	h := Handler(probe{false})
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/readyz", nil))
	if r.Code != 503 {
		t.Fatal(r.Code)
	}
}
