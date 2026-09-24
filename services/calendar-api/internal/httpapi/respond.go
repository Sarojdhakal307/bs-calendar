package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/events"
)

// Cache-Control policies (docs/api.md "Caching").
const (
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheManifest  = "public, max-age=30, s-maxage=30, stale-while-revalidate=300"
	cacheShort     = "public, max-age=60, s-maxage=60"
	cacheHour      = "public, max-age=3600, s-maxage=3600"
	cacheNoStore   = "no-store"
	cachePrivate   = "private, no-cache"
)

const (
	maxBody       = 1 << 20  // 1 MB
	maxImportBody = 10 << 20 // 10 MB
)

// writeJSON writes v as JSON. When etag is "", a weak ETag is derived from the body.
// Conditional GETs (If-None-Match) get 304.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any, cacheControl, etag string) {
	body, err := json.Marshal(v)
	if err != nil {
		writeProblem(w, r, apperr.Internal(err))
		return
	}
	writeBytes(w, r, status, "application/json; charset=utf-8", body, cacheControl, etag)
}

func weakETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `W/"` + hex.EncodeToString(sum[:8]) + `"`
}

func writeBytes(w http.ResponseWriter, r *http.Request, status int, contentType string, body []byte, cacheControl, etag string) {
	h := w.Header()
	if cacheControl != "" {
		h.Set("Cache-Control", cacheControl)
	}
	if status == http.StatusOK && r.Method == http.MethodGet {
		if etag == "" {
			etag = weakETag(body)
		}
		h.Set("ETag", etag)
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	} else if etag != "" {
		h.Set("ETag", etag)
	}
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	norm := func(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "W/") }
	for _, part := range strings.Split(header, ",") {
		if p := strings.TrimSpace(part); p == "*" || norm(p) == norm(etag) {
			return true
		}
	}
	return false
}

// problem is the RFC 9457 document.
type problem struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Code      string              `json:"code"`
	Detail    string              `json:"detail,omitempty"`
	Instance  string              `json:"instance"`
	RequestID string              `json:"requestId,omitempty"`
	Errors    []apperr.FieldError `json:"errors,omitempty"`
}

func writeProblem(w http.ResponseWriter, r *http.Request, err error) {
	e := apperr.From(err)
	ri := info(r)
	if e.Status >= 500 {
		slog.Default().Error("request failed", "err", err, "requestId", ri.id, "path", r.URL.Path)
	}
	doc := map[string]any{}
	base, _ := json.Marshal(problem{
		Type: "urn:bs-calendar:problem:" + e.Code, Title: e.Title, Status: e.Status, Code: e.Code,
		Detail: e.Detail, Instance: r.URL.Path, RequestID: ri.id, Errors: e.Fields,
	})
	_ = json.Unmarshal(base, &doc)
	for k, v := range e.Extra {
		if _, taken := doc[k]; !taken {
			doc[k] = v
		}
	}
	body, _ := json.Marshal(doc)
	h := w.Header()
	h.Set("Cache-Control", cacheNoStore)
	h.Set("Content-Type", "application/problem+json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(e.Status)
	_, _ = w.Write(body)
}

// decodeJSON reads a strict JSON body into v.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if err := requireContentType(r, "application/json"); err != nil {
		return err
	}
	b, err := readBody(w, r, maxBody)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return apperr.BadRequest("Request body is empty.")
		}
		return events.DecodeError(err)
	}
	if dec.More() {
		return apperr.BadRequest("Request body must contain a single JSON value.")
	}
	return nil
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, &apperr.Error{Status: http.StatusRequestEntityTooLarge, Code: apperr.CodePayloadTooLarge,
				Title: "Payload too large", Detail: "The request body exceeds " + strconv.FormatInt(limit, 10) + " bytes."}
		}
		return nil, apperr.BadRequest("Could not read request body.")
	}
	return b, nil
}

func requireContentType(r *http.Request, allowed ...string) error {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err == nil {
		for _, a := range allowed {
			if mt == a {
				return nil
			}
		}
	}
	return &apperr.Error{Status: http.StatusUnsupportedMediaType, Code: apperr.CodeUnsupportedMedia,
		Title: "Unsupported media type", Detail: "Content-Type must be " + strings.Join(allowed, " or ") + "."}
}

// ifMatch parses If-Match: "v3", W/"v3", "3" or 3. Absent or * returns nil.
func ifMatch(r *http.Request) (*int, error) {
	h := strings.TrimSpace(r.Header.Get("If-Match"))
	if h == "" || h == "*" {
		return nil, nil
	}
	s := strings.Trim(strings.TrimPrefix(h, "W/"), `"`)
	s = strings.TrimPrefix(s, "v")
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return nil, apperr.BadRequest(`If-Match must be a version tag such as "v3".`)
	}
	return &n, nil
}

func requireIfMatch(r *http.Request) (int, error) {
	v, err := ifMatch(r)
	if err != nil {
		return 0, err
	}
	if v == nil {
		return 0, apperr.PreconditionRequired()
	}
	return *v, nil
}

func queryInt(r *http.Request, name string, def, min, max int) (int, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, apperr.Validation(apperr.FieldError{Field: name, Message: "must be an integer from " + strconv.Itoa(min) + " to " + strconv.Itoa(max)})
	}
	return n, nil
}

func pathInt(r *http.Request, name string) (int, error) {
	n, err := strconv.Atoi(r.PathValue(name))
	if err != nil {
		return 0, apperr.BadRequest(name + " must be an integer")
	}
	return n, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
