package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/computeruse"
)

func runDesktopBridge(stdin io.Reader, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return computeruse.Bridge(ctx, os.Getenv("PLANTASK_UID"), stdin, stdout)
}

func startDesktopBroker(ctx context.Context, cfg runConfig) (context.Context, func(), error) {
	b := computeruse.New(cfg.Namespace, cfg.TaskName)
	l, err := computeruse.Listen(ctx, b, cfg.TaskUID)
	if err != nil {
		b.Close()
		return ctx, nil, err
	}
	return computeruse.WithBroker(ctx, b), func() { b.Close(); l.Close() }, nil
}
