// Package api embeds the OpenAPI contract so the server can serve it and tests can enforce it.
package api

import _ "embed"

// OpenAPI is the OpenAPI 3.1 document describing the calendar API.
//
//go:embed openapi.yaml
var OpenAPI []byte
