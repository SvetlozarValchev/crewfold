// Package webui embeds the production room console assets into the Crewfold
// binary. Node is a pinned build-time dependency only.
package webui

import "embed"

// Assets contains the content-hashed Vite production build.
//
//go:embed dist
var Assets embed.FS
