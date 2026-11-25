package httpapi

import (
	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"net/http"
	"time"
)

type RequestObservation struct {
	Method, Path, Platform, Operation string
	Status                            int
	Duration                          time.Duration
}

type observedResponse struct {
	http.ResponseWriter
	status int
}

func (r *observedResponse) WriteHeader(status int) {
	if r.status == 0 && status >= 200 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *observedResponse) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	return r.ResponseWriter.Write(data)
}

func (r *observedResponse) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) observe(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, func()) {
	if s.Observe == nil {
		return w, func() {}
	}
	start := time.Now()
	response := &observedResponse{ResponseWriter: w}
	return response, func() {
		status := response.status
		if status == 0 {
			status = 200
		}
		record := RequestObservation{Method: r.Method, Path: r.URL.Path, Status: status, Duration: time.Since(start)}
		q := r.URL.Query()
		platform, operation := normalized(q.Get("server")), normalized(q.Get("type"))
		if platform != "" && music.KnownPlatform(platform) {
			record.Platform = platform
			if music.Supports(platform, operation) {
				record.Operation = operation
			}
		}
		s.Observe(record)
	}
}
