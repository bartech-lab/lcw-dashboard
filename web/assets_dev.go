//go:build dev

package web

import (
	"io/fs"
	"os"
)

// Assets reads from disk under the dev tag, so a CSS or TSX change needs no Go
// rebuild.
func Assets() fs.FS { return os.DirFS("web/dist") }
