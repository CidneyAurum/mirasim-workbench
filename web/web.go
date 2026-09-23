// Package web 内嵌管理后台静态资源（embed.FS）。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:static
var embedded embed.FS

// FS 是管理后台静态资源根（index.html、app.js、style.css 直接在根下）。
var FS = mustSub(embedded, "static")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
