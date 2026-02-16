package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sourcegraph/jsonrpc2"
)

func NewHandler(noLinterName bool) jsonrpc2.Handler {
	handler := &langHandler{
		request:      make(chan DocumentURI),
		noLinterName: noLinterName,
	}
	go handler.linter()

	return jsonrpc2.HandlerWithError(handler.handle)
}

// pathConfig stores parsed golangci-lint command flags related to path handling.
type pathConfig struct {
	pathMode  string
	configDir string
	noConfig  bool
}

// parseCommandFlags extracts path-related flags from the golangci-lint command.
func parseCommandFlags(command []string) pathConfig {
	config := pathConfig{}

	for i, arg := range command {
		arg = strings.TrimPrefix(arg, "--")

		if after, ok := strings.CutPrefix(arg, "path-mode="); ok {
			config.pathMode = after
		} else if arg == "path-mode" && i+1 < len(command) {
			config.pathMode = command[i+1]
		}

		if after, ok := strings.CutPrefix(arg, "config="); ok {
			configPath := after
			config.configDir = filepath.Dir(configPath)
		} else if arg == "config" && i+1 < len(command) {
			config.configDir = filepath.Dir(command[i+1])
		}

		if arg == "no-config" {
			config.noConfig = true
		}
	}

	return config
}

// getBaseDir returns the base directory for resolving relative paths.
func (pc pathConfig) getBaseDir(cmdDir, rootDir string) string {
	if pc.pathMode == "abs" {
		return ""
	}

	if pc.noConfig {
		return cmdDir
	}

	if pc.configDir != "" {
		return pc.configDir
	}

	return rootDir
}

type langHandler struct {
	conn         *jsonrpc2.Conn
	request      chan DocumentURI
	command      []string
	noLinterName bool
	pathConfig   pathConfig

	rootURI string
	rootDir string
}

// As defined in the `golangci-lint` source code:
// https://github.com/golangci/golangci-lint/blob/main/pkg/exitcodes/exitcodes.go#L24
const GoNoFilesExitCode = 5

func (h *langHandler) errToDiagnostics(err error) []Diagnostic {
	var message string
	switch e := err.(type) {
	case *exec.ExitError:
		if e.ExitCode() == GoNoFilesExitCode {
			return []Diagnostic{}
		}
		message = string(e.Stderr)
	default:
		slog.Debug("error converting to diagnostics", "message", message)
		message = e.Error()
	}
	return []Diagnostic{
		{Severity: DSError, Message: message},
	}
}

func (h *langHandler) lint(uri DocumentURI) ([]Diagnostic, error) {
	diagnostics := make([]Diagnostic, 0)

	path := uriToPath(string(uri))
	dir, _ := filepath.Split(path)

	args := make([]string, 0, len(h.command))
	args = append(args, h.command[1:]...)
	args = append(args, dir)
	cmd := exec.Command(h.command[0], args...)
	if strings.HasPrefix(path, h.rootDir) {
		cmd.Dir = h.rootDir
	} else {
		cmd.Dir = dir
	}

	slog.Debug("running golangci-lint", "command", cmd.Args)

	b, err := cmd.Output()
	if err == nil {
		return diagnostics, nil
	} else if len(b) == 0 {
		// golangci-lint would output critical error to stderr rather than stdout
		// https://github.com/nametake/golangci-lint-langserver/issues/24
		return h.errToDiagnostics(err), nil
	}

	var result GolangCILintResult
	if err := json.Unmarshal(b, &result); err != nil {
		return h.errToDiagnostics(err), nil
	}

	slog.Debug("lint result", "result", result)

	// Get absolute path of the target file for comparison.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return h.errToDiagnostics(err), nil
	}

	// Clean the path to ensure consistent comparison.
	absPath = filepath.Clean(absPath)

	// Determine base directory for resolving relative paths.
	baseDir := h.pathConfig.getBaseDir(cmd.Dir, h.rootDir)

	for _, issue := range result.Issues {
		issuePath := issue.Pos.Filename

		// Convert issue path to absolute path for comparison.
		if !filepath.IsAbs(issuePath) {
			// Join with base directory and convert to absolute.
			candidatePath := filepath.Join(baseDir, issuePath)
			absIssuePath, err := filepath.Abs(candidatePath)
			if err != nil {
				continue
			}

			absIssuePath = filepath.Clean(absIssuePath)

			// If direct join doesn't match, try fallback suffix matching.
			// This handles cases where a global config exists but wasn't explicitly specified.
			if absIssuePath != absPath {
				issueBase := filepath.Base(issuePath)
				targetBase := filepath.Base(absPath)

				if issueBase != targetBase {
					continue
				}

				if !strings.HasSuffix(absPath, issuePath) {
					continue
				}
			}
		} else {
			// Path is already absolute, clean it for comparison.
			absIssuePath := filepath.Clean(issuePath)
			if absIssuePath != absPath {
				continue
			}
		}

		d := Diagnostic{
			Range: Range{
				Start: Position{
					Line:      max(issue.Pos.Line-1, 0),
					Character: max(issue.Pos.Column-1, 0),
				},
				End: Position{
					Line:      max(issue.Pos.Line-1, 0),
					Character: max(issue.Pos.Column-1, 0),
				},
			},
			Severity: issue.DiagSeverity(),
			Source:   &issue.FromLinter,
			Message:  h.diagnosticMessage(&issue),
		}
		diagnostics = append(diagnostics, d)
	}

	return diagnostics, nil
}

func (h *langHandler) diagnosticMessage(issue *Issue) string {
	if h.noLinterName {
		return issue.Text
	}

	return fmt.Sprintf("%s: %s", issue.FromLinter, issue.Text)
}

func (h *langHandler) linter() {
	for {
		uri, ok := <-h.request
		if !ok {
			break
		}

		diagnostics, err := h.lint(uri)
		if err != nil {
			slog.Error("lint error", "error", err)

			continue
		}

		if err := h.conn.Notify(
			context.Background(),
			"textDocument/publishDiagnostics",
			&PublishDiagnosticsParams{
				URI:         uri,
				Diagnostics: diagnostics,
			}); err != nil {
			slog.Error("failed to publish diagnostics", "error", err)
		}
	}
}

func (h *langHandler) handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (result any, err error) {
	slog.Debug("handling request", "method", req.Method)

	switch req.Method {
	case "initialize":
		return h.handleInitialize(ctx, conn, req)
	case "initialized":
		return
	case "shutdown":
		return h.handleShutdown(ctx, conn, req)
	case "textDocument/didOpen":
		return h.handleTextDocumentDidOpen(ctx, conn, req)
	case "textDocument/didClose":
		return h.handleTextDocumentDidClose(ctx, conn, req)
	case "textDocument/didChange":
		return h.handleTextDocumentDidChange(ctx, conn, req)
	case "textDocument/didSave":
		return h.handleTextDocumentDidSave(ctx, conn, req)
	case "workspace/didChangeConfiguration":
		return h.handlerWorkspaceDidChangeConfiguration(ctx, conn, req)
	}

	return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: fmt.Sprintf("method not supported: %s", req.Method)}
}

func (h *langHandler) handleInitialize(_ context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (result any, err error) {
	var params InitializeParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		return nil, err
	}

	h.rootURI = params.RootURI
	h.rootDir = uriToPath(params.RootURI)
	h.conn = conn
	h.command = params.InitializationOptions.Command

	// Parse path-related flags from the command.
	h.pathConfig = parseCommandFlags(h.command)

	return InitializeResult{
		Capabilities: ServerCapabilities{
			TextDocumentSync: TextDocumentSyncOptions{
				Change:    TDSKNone,
				OpenClose: true,
				Save:      true,
			},
		},
	}, nil
}

func (h *langHandler) handleShutdown(_ context.Context, _ *jsonrpc2.Conn, _ *jsonrpc2.Request) (result any, err error) {
	close(h.request)

	return nil, nil
}

func (h *langHandler) handleTextDocumentDidOpen(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (result any, err error) {
	var params DidOpenTextDocumentParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		return nil, err
	}

	h.request <- params.TextDocument.URI

	return nil, nil
}

func (h *langHandler) handleTextDocumentDidClose(_ context.Context, _ *jsonrpc2.Conn, _ *jsonrpc2.Request) (result any, err error) {
	return nil, nil
}

func (h *langHandler) handleTextDocumentDidChange(_ context.Context, _ *jsonrpc2.Conn, _ *jsonrpc2.Request) (result any, err error) {
	return nil, nil
}

func (h *langHandler) handleTextDocumentDidSave(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (result any, err error) {
	var params DidSaveTextDocumentParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		return nil, err
	}

	h.request <- params.TextDocument.URI

	return nil, nil
}

func (h *langHandler) handlerWorkspaceDidChangeConfiguration(_ context.Context, _ *jsonrpc2.Conn, _ *jsonrpc2.Request) (result any, err error) {
	return nil, nil
}
