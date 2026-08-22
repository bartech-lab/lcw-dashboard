//go:build !dev

package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

func Assets() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil
	}
	return sub
}
