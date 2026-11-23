// Package trellis holds what lives at the repository root: the built-in
// packs and the blueprints it offers a catalog.
package trellis

import (
	"embed"
	"io/fs"
)

// Blueprints is the blueprints.yaml at the repository root: the finding and
// waiver lists trellis writes and reads.
//
//go:embed blueprints.yaml
var Blueprints []byte

//go:embed packs/*.yaml
var packs embed.FS

// Packs are the built-in packs, one file each.
func Packs() fs.FS {
	sub, err := fs.Sub(packs, "packs")
	if err != nil {
		panic(err)
	}
	return sub
}
