package status

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/*
var adminWebAssets embed.FS

func (s *Server) adminWeb(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.URL.Path == "/admin" {
		http.Redirect(writer, request, "/admin/", http.StatusPermanentRedirect)
		return
	}
	if !strings.HasPrefix(request.URL.Path, "/admin/") {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
			"connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'",
	)
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	http.StripPrefix("/admin/", adminWebFileServer()).ServeHTTP(writer, request)
}

func adminWebFileServer() http.Handler {
	assets, err := fs.Sub(adminWebAssets, "web")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(assets))
}
