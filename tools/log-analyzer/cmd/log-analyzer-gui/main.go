//go:build gui

package main

import (
	"cursor-log-analyzer/internal/gui"
	"embed"
	"fmt"
	"os"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if err := gui.Run(gui.Resources{Assets: assets}); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "启动日志分析器失败: %v\n", err)
		os.Exit(1)
	}
}
