package httpapi

import (
	"atlas-render/internal/engine"
	"atlas-render/internal/model"
	"atlas-render/internal/service"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

type API struct {
	Service *service.Service
	Worker  *engine.Worker
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// wantsDiagnostics reports whether the client opted into the structured error
// protocol. The choice is per request; old clients keep receiving the legacy
// {"code","error"} envelope unchanged. An explicit Atlas-Diagnostics: v1 asks
// for the legacy representation even if the Accept header would opt in, which
// keeps header-precedence unambiguous for clients that set both.
func wantsDiagnostics(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Atlas-Diagnostics"))) {
	case "v2":
		return true
	case "v1", "legacy":
		return false
	}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mediaType, _, e := mime.ParseMediaType(strings.TrimSpace(part))
		if e != nil {
			continue
		}
		if strings.EqualFold(mediaType, model.DiagnosticsMediaType) {
			return true
		}
	}
	return false
}

func fail(w http.ResponseWriter, r *http.Request, e error) {
	var de *model.Error
	if !errors.As(e, &de) {
		write(w, 500, map[string]string{"code": "internal", "error": "operation could not be committed"})
		return
	}
	report := de.Report()
	if report != nil {
		// The representation depends on the negotiation headers for every
		// response that has a v2 form, including the legacy fallback below, so
		// a shared cache cannot replay one form for the other.
		w.Header().Set("Vary", "Accept, Atlas-Diagnostics")
	}
	if report != nil && wantsDiagnostics(r) {
		w.Header().Set("Content-Type", model.DiagnosticsMediaType)
		w.WriteHeader(de.Status)
		_ = json.NewEncoder(w).Encode(report)
		return
	}
	write(w, de.Status, map[string]string{"code": de.Code, "error": de.Message})
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 12<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		return model.Invalid("invalid JSON: %v", e)
	}
	var extra any
	if e := d.Decode(&extra); !errors.Is(e, io.EOF) {
		return model.Invalid("expected exactly one JSON value")
	}
	return nil
}
func (a API) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]string{"status": "ready"})
	})
	m.HandleFunc("PUT /v1/scenes/{id}", a.putScene)
	m.HandleFunc("GET /v1/scenes/{id}", a.getScene)
	m.HandleFunc("POST /v1/scenes/{id}/state", a.sceneState)
	m.HandleFunc("POST /v1/scenes/{id}/snapshots", a.freeze)
	m.HandleFunc("PUT /v1/assets/{id}", a.importAsset)
	m.HandleFunc("POST /v1/assets/{id}/retire", a.retireAsset)
	m.HandleFunc("GET /v1/snapshots/{id}", a.getSnapshot)
	m.HandleFunc("POST /v1/snapshots/{id}/revoke", a.revoke)
	m.HandleFunc("GET /v1/snapshots/{id}/package", a.exportPackage)
	m.HandleFunc("POST /v1/imports/{id}", a.importPackage)
	m.HandleFunc("POST /v1/jobs", a.submit)
	m.HandleFunc("GET /v1/jobs/{id}", a.getJob)
	m.HandleFunc("POST /v1/jobs/{id}/cancel", a.cancel)
	m.HandleFunc("POST /v1/jobs/{id}/retry", a.retry)
	m.HandleFunc("GET /v1/artifacts/{id}", a.download)
	m.HandleFunc("DELETE /v1/artifacts/{id}", a.purge)
	m.HandleFunc("POST /v1/compare", a.compare)
	m.HandleFunc("GET /v1/status", a.status)
	m.HandleFunc("GET /v1/events", a.events)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		m.ServeHTTP(w, r)
	})
}
