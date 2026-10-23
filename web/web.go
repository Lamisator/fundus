// Package web embeds the browser UI, the share-page template and the
// OpenAPI document.
package web

import "embed"

//go:embed static templates openapi.yaml
var FS embed.FS
