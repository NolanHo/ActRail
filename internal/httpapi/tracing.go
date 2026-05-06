package httpapi

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(payload []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(payload)
}

func (r *statusRecorder) Flush() {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (r *statusRecorder) Push(target string, opts *http.PushOptions) error {
	pusher, ok := r.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if readerFrom, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

func traceMiddleware(next http.Handler) http.Handler {
	tracer := otel.Tracer("actrail/http")
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		operation := req.Method + " " + req.URL.Path
		ctx, span := tracer.Start(req.Context(), operation, trace.WithAttributes(
			attribute.String("http.request.method", req.Method),
			attribute.String("url.path", req.URL.Path),
			attribute.String("url.query", req.URL.RawQuery),
		))
		defer span.End()
		traceID := span.SpanContext().TraceID().String()
		if traceID == "00000000000000000000000000000000" {
			traceID = ""
		}
		w.Header().Set("X-Trace-Id", traceID)
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, req.WithContext(ctx))
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	})
}

func traceIDFromRequest(req *http.Request) string {
	if req == nil {
		return ""
	}
	span := trace.SpanContextFromContext(req.Context())
	if !span.IsValid() {
		return ""
	}
	return strings.TrimSpace(span.TraceID().String())
}
