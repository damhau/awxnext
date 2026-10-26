// Package ui embeds the built web UI (npm run build writes ui/dist).
package ui

import "embed"

//go:embed all:dist
var Dist embed.FS
