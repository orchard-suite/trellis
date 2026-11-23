// Package web is the board's page and its assets, embedded in the binary.
package web

import (
	"embed"
	_ "embed"
)

//go:embed static
var Static embed.FS

//go:embed index.html
var Index []byte

// App is what a device reads to install the board as an app: its icons and
// its manifest, served at the site root.
//
//go:embed app
var App embed.FS
