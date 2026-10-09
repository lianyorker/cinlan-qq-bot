package status

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform/qqnt"
)

type fakeNativeLogin struct {
	fakeActionAdapter
	qr      qqnt.LoginQR
	refresh bool
}

func (f *fakeNativeLogin) LoginStatus() qqnt.RuntimeInfo {
	return qqnt.RuntimeInfo{State: "waiting_login", LastError: "test-error"}
}
func (f *fakeNativeLogin) LoginQRCode(_ context.Context, refresh bool) (qqnt.LoginQR, error) {
	f.refresh = refresh
	return f.qr, f.err
}

func TestNativeLoginAPI(t *testing.T) {
	f := &fakeNativeLogin{qr: qqnt.LoginQR{State: "waiting_login", Status: "available", PNGBase64: "test-png"}}
	s := NewWithOptions("127.0.0.1:0", fakeConnection(false), fakeStats{}, AdminOptions{Token: "test-token", Actions: f})
	for _, tc := range []struct {
		path, token, method string
		code                int
	}{
		{"login/qr", "", "GET", 401}, {"login/status", "wrong", "GET", 401},
		{"login/status", "test-token", "GET", 200}, {"login/qr?refresh=true", "test-token", "GET", 200},
		{"login/qr?refresh=bad", "test-token", "GET", 400}, {"login/qr", "test-token", "POST", 405},
	} {
		r := httptest.NewRequest(tc.method, "/api/v1/"+tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s = %d: %s", tc.path, w.Code, w.Body.String())
		}
		if tc.path == "login/status" && w.Code == 200 && strings.Contains(w.Body.String(), "test-png") {
			t.Fatal("QR leaked to status API")
		}
		if tc.path == "login/qr?refresh=true" && (!f.refresh || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("refresh/cache contract missing")
		}
	}
	f.err = errors.New("QR unavailable")
	r := httptest.NewRequest("GET", "/api/v1/login/qr", nil)
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "QR unavailable") {
		t.Fatal(w.Body.String())
	}
	s.admin.Actions = &fakeActionAdapter{}
	w = httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("non-native login = %d", w.Code)
	}
}
