// Package fixtures embeds the shared data files used by the Go service and its tests.
// The same files are consumed by the TypeScript packages, so they are the single source of truth.
package fixtures

import _ "embed"

// YearTableSeed is the initial BS year table with provenance (see the file header).
//
//go:embed year-table.seed.json
var YearTableSeed []byte

// Conversions holds golden AD<->BS pairs from sources independent of the seed table.
//
//go:embed conversions.json
var Conversions []byte

// UIConfigSchema is the JSON Schema for admin-controlled UI configuration.
//
//go:embed ui-config.schema.json
var UIConfigSchema []byte

// UIConfigDefault is the built-in UI configuration served as version 0.
//
//go:embed ui-config.default.json
var UIConfigDefault []byte
