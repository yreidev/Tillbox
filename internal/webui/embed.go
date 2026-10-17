// Package webui 把构建好的前端（web/ 构建到 dist/）打进二进制。
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS 返回前端文件。没有构建前端时只有一个 .gitkeep。
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
