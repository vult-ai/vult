//go:build !webui

package web

import "testing"

func TestDefaultBuildOmitsBrowserFiles(t *testing.T) {
	if App() != nil {
		t.Fatal("default build unexpectedly enabled browser files")
	}
}
