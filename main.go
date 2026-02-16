package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/sourcegraph/jsonrpc2"
)

var defaultSeverity = "Warn"

func main() {
	debug := flag.Bool("debug", false, "output debug log")
	noLinterName := flag.Bool("nolintername", false, "don't show a linter name in message")
	flag.StringVar(&defaultSeverity, "severity", defaultSeverity, "Default severity to use. Choices are: Err(or), Warn(ing), Info(rmation) or Hint")

	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)

	handler := NewHandler(*noLinterName)

	var connOpt []jsonrpc2.ConnOpt

	slog.Info("golangci-lint-langserver: connections opened")

	<-jsonrpc2.NewConn(
		context.Background(),
		jsonrpc2.NewBufferedStream(stdrwc{}, jsonrpc2.VSCodeObjectCodec{}),
		handler,
		connOpt...,
	).DisconnectNotify()

	slog.Info("golangci-lint-langserver: connections closed")
}

type stdrwc struct{}

func (stdrwc) Read(p []byte) (int, error) {
	return os.Stdin.Read(p)
}

func (stdrwc) Write(p []byte) (int, error) {
	return os.Stdout.Write(p)
}

func (stdrwc) Close() error {
	if err := os.Stdin.Close(); err != nil {
		return err
	}

	return os.Stdout.Close()
}
