// Package contracts exposes the platform's API, event, file, and data
// contracts as an embedded filesystem so every service validates against the
// same frozen artefacts it was built with.
//
// Released contract files are immutable: any change ships as a new vN file.
// CI enforces this (see .github/workflows/contracts-ci.yml).
package contracts

import "embed"

// FS contains every contract artefact: OpenAPI specs, JSON Schemas (event
// envelope, event payloads, ingestion snapshots, decisioning documents),
// SFTP file-feed contracts, CDC source specs, registries, the AsyncAPI
// topic index, golden examples, and shared test vectors.
//
//go:embed all:openapi all:schemas all:files all:cdc all:registries all:asyncapi all:examples all:testdata
var FS embed.FS
