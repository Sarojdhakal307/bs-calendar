# API changelog

All notable changes to the HTTP API (`api/openapi.yaml`). The API version is `info.version`
in the spec: minor for additions, patch for documentation or fixes. Breaking changes ship as a
new URL prefix (`/v2`) and are announced here at least 6 months ahead (docs/api.md §11.2).

## 1.0.0 — 2026-09-24

First release of `/v1`.

**Client API**
- `GET /v1/manifest` with immutable, versioned resources: year table, UI config and BS-year event buckets.
- Year-table checksum over a canonical text form (`<y>:<start>:<d1>,…,<d12>:<status>\n`), verifiable in any language.
- Staged UI-config rollouts (`config.candidate`) and client compatibility (`minClientVersion`, `updateRequired`).
- `GET /v1/convert`, `/v1/today`, `/v1/months/{basis}/{year}/{month}` for thin clients.
- `GET /v1/events` (range, max 366 days), `/v1/events.ics` (subscription feed), `/v1/categories`.
- `POST /v1/telemetry` for anonymous client health signals.

**Admin API**
- Password login with 15-minute JWT access tokens and rotating refresh tokens with reuse detection.
- Categories and events: drafts, JSON Merge Patch with `If-Match`, publish/archive/soft delete/restore,
  recurrence (`yearly_bs`, `yearly_ad`, RFC 5545 `rrule`), CSV import with dry run, copy to next year.
- Year table drafts with validation, impact report and four-eyes approval.
- UI config drafts with JSON Schema and WCAG contrast validation, four-eyes review, staged rollout, rollback.
- API keys with origin allow-lists and rate limits; signed webhooks with retries; users and roles; audit log; data health.
