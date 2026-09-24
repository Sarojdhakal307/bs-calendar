// Package apperr defines the error type shared by services and mapped to
// application/problem+json (RFC 9457) by the HTTP layer. Codes are part of the
// public API contract; see docs/api.md "Error codes".
package apperr

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"bscalendar/services/calendar-api/internal/bscal"
)

// Stable error codes.
const (
	CodeBadRequest           = "BAD_REQUEST"
	CodeValidation           = "VALIDATION_FAILED"
	CodeInvalidDate          = "INVALID_DATE"
	CodeOutOfRange           = "OUT_OF_RANGE"
	CodeNotFound             = "NOT_FOUND"
	CodeVersionNotFound      = "VERSION_NOT_FOUND"
	CodeUnauthorized         = "UNAUTHORIZED"
	CodeInvalidCredentials   = "INVALID_CREDENTIALS"
	CodeInvalidAPIKey        = "INVALID_API_KEY"
	CodeTokenReused          = "TOKEN_REUSED"
	CodeForbidden            = "FORBIDDEN"
	CodeOriginNotAllowed     = "ORIGIN_NOT_ALLOWED"
	CodeFourEyes             = "FOUR_EYES_REQUIRED"
	CodeConflict             = "CONFLICT"
	CodeInvalidState         = "INVALID_STATE"
	CodeVersionConflict      = "VERSION_CONFLICT"
	CodePreconditionRequired = "PRECONDITION_REQUIRED"
	CodePayloadTooLarge      = "PAYLOAD_TOO_LARGE"
	CodeUnsupportedMedia     = "UNSUPPORTED_MEDIA_TYPE"
	CodeRateLimited          = "RATE_LIMITED"
	CodeInternal             = "INTERNAL"
	CodeUnavailable          = "SERVICE_UNAVAILABLE"
)

// FieldError points at one invalid input field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error is an API-facing error.
type Error struct {
	Status int
	Code   string
	Title  string
	Detail string
	Fields []FieldError
	Extra  map[string]any // extension members added to the problem document
	Err    error          // wrapped cause, never shown to clients
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Detail, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func (e *Error) Unwrap() error { return e.Err }

// With adds an extension member.
func (e *Error) With(key string, v any) *Error {
	if e.Extra == nil {
		e.Extra = map[string]any{}
	}
	e.Extra[key] = v
	return e
}

func newErr(status int, code, title, detail string) *Error {
	return &Error{Status: status, Code: code, Title: title, Detail: detail}
}

// BadRequest is a malformed request.
func BadRequest(detail string) *Error {
	return newErr(http.StatusBadRequest, CodeBadRequest, "Bad request", detail)
}

// Validation reports invalid fields.
func Validation(fields ...FieldError) *Error {
	e := newErr(http.StatusUnprocessableEntity, CodeValidation, "Validation failed", "One or more fields are invalid.")
	e.Fields = fields
	return e
}

// NotFound is a missing resource.
func NotFound(what string) *Error {
	return newErr(http.StatusNotFound, CodeNotFound, "Not found", what+" not found")
}

// Unauthorized is missing or invalid authentication.
func Unauthorized(code, detail string) *Error {
	return newErr(http.StatusUnauthorized, code, "Unauthorized", detail)
}

// Forbidden is an authenticated caller without permission.
func Forbidden(code, detail string) *Error {
	return newErr(http.StatusForbidden, code, "Forbidden", detail)
}

// Conflict is a state or uniqueness conflict.
func Conflict(code, detail string) *Error {
	return newErr(http.StatusConflict, code, "Conflict", detail)
}

// InvalidState is an action not allowed in the resource's current state.
func InvalidState(detail string) *Error {
	return newErr(http.StatusConflict, CodeInvalidState, "Invalid state", detail)
}

// VersionConflict is a stale If-Match.
func VersionConflict(current int) *Error {
	return newErr(http.StatusPreconditionFailed, CodeVersionConflict, "Version conflict",
		"The resource was changed by someone else. Reload it and try again.").With("currentVersion", current)
}

// PreconditionRequired is a missing If-Match.
func PreconditionRequired() *Error {
	return newErr(http.StatusPreconditionRequired, CodePreconditionRequired, "Precondition required",
		`Send If-Match with the resource version, for example If-Match: "v3".`)
}

// Internal wraps an unexpected error.
func Internal(err error) *Error {
	e := newErr(http.StatusInternalServerError, CodeInternal, "Internal error", "An unexpected error occurred.")
	e.Err = err
	return e
}

// From converts any error to *Error.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var ve *bscal.ValidationError
	switch {
	case errors.As(err, &ve):
		out := newErr(http.StatusUnprocessableEntity, CodeValidation, "Invalid year table", ve.Error())
		out.Extra = map[string]any{"issues": ve.Issues}
		return out
	case errors.Is(err, bscal.ErrOutOfRange):
		return newErr(http.StatusUnprocessableEntity, CodeOutOfRange, "Date out of range", err.Error())
	case errors.Is(err, bscal.ErrInvalidDate):
		return newErr(http.StatusUnprocessableEntity, CodeInvalidDate, "Invalid date", err.Error())
	case errors.Is(err, pgx.ErrNoRows):
		return NotFound("resource")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			e := Conflict(CodeConflict, "A resource with the same unique value already exists.")
			e.Err = err
			return e.With("constraint", pgErr.ConstraintName)
		case "23503":
			e := Conflict(CodeConflict, "The resource is referenced by, or references, another resource.")
			e.Err = err
			return e.With("constraint", pgErr.ConstraintName)
		case "23514":
			e := newErr(http.StatusUnprocessableEntity, CodeValidation, "Validation failed", "A database constraint rejected the value.")
			e.Err = err
			return e.With("constraint", pgErr.ConstraintName)
		}
	}
	return Internal(err)
}
