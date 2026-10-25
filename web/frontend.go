// Package web holds the built Vue app (frontend/dist), embedded into the binary.
package web

import "embed"

//go:embed all:frontend/dist
var Dist embed.FS
