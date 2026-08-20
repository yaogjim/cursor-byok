//go:build !windows

package client

import "os"

func replaceExportFile(sourcePath, targetPath string) error {
	return os.Rename(sourcePath, targetPath)
}
