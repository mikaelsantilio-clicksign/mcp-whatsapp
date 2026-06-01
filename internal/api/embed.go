package api

import "embed"

//go:embed templates/*.html
var webFS embed.FS
