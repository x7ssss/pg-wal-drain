package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoEmDashes(t *testing.T) {
	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := filepath.Ext(path)
		if ext == ".exe" || ext == "" && strings.Contains(path, "dist") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		content := string(data)
		if strings.Contains(content, "\u2014") {
			t.Errorf("file %s contains em-dash (\\u2014)", path)
		}
		if strings.Contains(content, "\u2013") {
			t.Errorf("file %s contains en-dash (\\u2013)", path)
		}

		return nil
	})

	if err != nil {
		t.Fatalf("filepath.Walk failed: %v", err)
	}
}
