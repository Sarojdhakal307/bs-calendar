// Package web embeds the public website (site/) and the admin dashboard (admin/) so the API binary
// serves them itself: one image, one port.
package web

import (
	"embed"
	"io/fs"
)

//go:embed site admin
var files embed.FS

// Site is the public website served at /.
func Site() fs.FS { return mustSub("site") }

// Admin is the admin dashboard served at /admin/.
func Admin() fs.FS { return mustSub("admin") }

func mustSub(dir string) fs.FS {
	f, err := fs.Sub(files, dir)
	if err != nil {
		panic(err)
	}
	return f
}
