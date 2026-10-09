package status

import (
	"context"
	"net/http"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform/qqnt"
)

type nativeLogin interface {
	LoginStatus() qqnt.RuntimeInfo
	LoginQRCode(context.Context, bool) (qqnt.LoginQR, error)
}

func (s *Server) loginAPI(w http.ResponseWriter, r *http.Request, path string) {
	if !allowReadMethod(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	runtime, ok := s.admin.Actions.(nativeLogin)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "native login runtime unavailable"})
		return
	}
	if path == "login/status" {
		writeJSON(w, http.StatusOK, runtime.LoginStatus())
		return
	}
	refresh := r.URL.Query().Get("refresh")
	if refresh != "" && refresh != "true" && refresh != "false" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "refresh must be true or false"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	qr, err := runtime.LoginQRCode(ctx, refresh == "true")
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error(), "state": runtime.LoginStatus().State})
		return
	}
	writeJSON(w, http.StatusOK, qr)
}
