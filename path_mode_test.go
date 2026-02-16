package main

import (
	"path/filepath"
	"testing"
)

// TestParseCommandFlags tests the parsing of golangci-lint command flags.
func TestParseCommandFlags(t *testing.T) {
	tests := []struct {
		name     string
		command  []string
		expected pathConfig
	}{
		{
			name:     "no flags",
			command:  []string{"golangci-lint", "run"},
			expected: pathConfig{},
		},
		{
			name:     "path-mode=abs with equals",
			command:  []string{"golangci-lint", "run", "--path-mode=abs"},
			expected: pathConfig{pathMode: "abs"},
		},
		{
			name:     "path-mode=abs separate",
			command:  []string{"golangci-lint", "run", "--path-mode", "abs"},
			expected: pathConfig{pathMode: "abs"},
		},
		{
			name:    "config with equals",
			command: []string{"golangci-lint", "run", "--config=/path/to/.golangci.yml"},
			expected: pathConfig{
				configDir: filepath.Dir("/path/to/.golangci.yml"),
			},
		},
		{
			name:    "config separate",
			command: []string{"golangci-lint", "run", "--config", "/path/to/.golangci.yml"},
			expected: pathConfig{
				configDir: filepath.Dir("/path/to/.golangci.yml"),
			},
		},
		{
			name:     "no-config flag",
			command:  []string{"golangci-lint", "run", "--no-config"},
			expected: pathConfig{noConfig: true},
		},
		{
			name: "combined flags",
			command: []string{
				"golangci-lint", "run",
				"--config=/path/to/.golangci.yml",
				"--path-mode=abs",
				"--no-config",
			},
			expected: pathConfig{
				pathMode:  "abs",
				configDir: filepath.Dir("/path/to/.golangci.yml"),
				noConfig:  true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseCommandFlags(tt.command)
			if result.pathMode != tt.expected.pathMode {
				t.Errorf("pathMode: expected %q, got %q", tt.expected.pathMode, result.pathMode)
			}
			if result.configDir != tt.expected.configDir {
				t.Errorf("configDir: expected %q, got %q", tt.expected.configDir, result.configDir)
			}
			if result.noConfig != tt.expected.noConfig {
				t.Errorf("noConfig: expected %v, got %v", tt.expected.noConfig, result.noConfig)
			}
		})
	}
}

// TestPathConfigGetBaseDir tests the getBaseDir method.
func TestPathConfigGetBaseDir(t *testing.T) {
	tests := []struct {
		name     string
		config   pathConfig
		cmdDir   string
		rootDir  string
		expected string
	}{
		{
			name:     "path-mode=abs returns empty",
			config:   pathConfig{pathMode: "abs"},
			cmdDir:   "/cmd",
			rootDir:  "/root",
			expected: "",
		},
		{
			name:     "no-config uses cmdDir",
			config:   pathConfig{noConfig: true},
			cmdDir:   "/cmd",
			rootDir:  "/root",
			expected: "/cmd",
		},
		{
			name:     "configDir set uses configDir",
			config:   pathConfig{configDir: "/config"},
			cmdDir:   "/cmd",
			rootDir:  "/root",
			expected: "/config",
		},
		{
			name:     "default uses rootDir",
			config:   pathConfig{},
			cmdDir:   "/cmd",
			rootDir:  "/root",
			expected: "/root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.getBaseDir(tt.cmdDir, tt.rootDir)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestPathComparisonLogic tests the path comparison logic used for handling
// different golangci-lint path-mode settings.
func TestPathComparisonLogic(t *testing.T) {
	tests := []struct {
		name        string
		targetPath  string
		issuePath   string
		baseDir     string
		shouldMatch bool
	}{
		{
			name:        "absolute path - match",
			targetPath:  "/project/src/main.go",
			issuePath:   "/project/src/main.go",
			baseDir:     "/project",
			shouldMatch: true,
		},
		{
			name:        "absolute path - no match",
			targetPath:  "/project/src/main.go",
			issuePath:   "/project/src/other.go",
			baseDir:     "/project",
			shouldMatch: false,
		},
		{
			name:        "relative to baseDir - match",
			targetPath:  "/project/src/main.go",
			issuePath:   "src/main.go",
			baseDir:     "/project",
			shouldMatch: true,
		},
		{
			name:        "relative to baseDir - no match",
			targetPath:  "/project/src/main.go",
			issuePath:   "other/main.go",
			baseDir:     "/project",
			shouldMatch: false,
		},
		{
			name:        "nested directory - relative to baseDir",
			targetPath:  "/project/foo/bar/baz.go",
			issuePath:   "foo/bar/baz.go",
			baseDir:     "/project",
			shouldMatch: true,
		},
		{
			name:        "basename mismatch",
			targetPath:  "/project/src/main.go",
			issuePath:   "src/other.go",
			baseDir:     "/project",
			shouldMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			absPath, err := filepath.Abs(tt.targetPath)
			if err != nil {
				t.Fatalf("filepath.Abs error: %v", err)
			}
			absPath = filepath.Clean(absPath)

			issuePath := tt.issuePath
			match := false

			if !filepath.IsAbs(issuePath) {
				candidatePath := filepath.Join(tt.baseDir, issuePath)
				absIssuePath, err := filepath.Abs(candidatePath)
				if err == nil {
					absIssuePath = filepath.Clean(absIssuePath)
					match = absIssuePath == absPath
				}
			} else {
				absIssuePath := filepath.Clean(issuePath)
				match = absIssuePath == absPath
			}

			if match != tt.shouldMatch {
				t.Errorf("path comparison failed: absPath=%s, issuePath=%s, baseDir=%s, expected match=%v, got match=%v",
					absPath, tt.issuePath, tt.baseDir, tt.shouldMatch, match)
			}
		})
	}
}
