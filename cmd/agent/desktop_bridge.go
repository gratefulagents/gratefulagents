package main

import (
	"context"
	"io"
	"log"
	"os"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/computeruse"
)

func runDesktopBridge(stdin io.Reader, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return computeruse.Bridge(ctx, os.Getenv("PLANTASK_UID"), stdin, stdout)
}

// startDesktopBroker publishes the run-scoped desktop relay. Supervised
// computer use is optional: when the private socket cannot be created the run
// proceeds without the computer_use tool instead of failing outright.
func startDesktopBroker(ctx context.Context, cfg runConfig) (context.Context, func()) {
	b := computeruse.New(cfg.Namespace, cfg.TaskName)
	l, err := computeruse.Listen(ctx, b, cfg.TaskUID)
	if err != nil {
		b.Close()
		log.Printf("desktop relay unavailable; continuing without computer use: %v", err)
		return ctx, func() {}
	}
	return computeruse.WithBroker(ctx, b), func() { b.Close(); l.Close() }
}
