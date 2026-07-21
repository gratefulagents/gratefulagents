package main

import (
	"fmt"
	"io"
	"os"
)

var runEntry = runChat

var slackEntry = runSlack

func runCLI(args []string, stderr io.Writer) int {
	if len(args) < 2 {
		_, _ = fmt.Fprintln(stderr, "Usage: agent <run|slack>")
		return 1
	}

	// Make the injected toolkit usable on arbitrary runtime images before any
	// subprocess is spawned (PATH assembly, sandbox propagation, CA bundle).
	setupToolkitEnv()
	// Propagate the run's git identity (GIT_AUTHOR_* / GIT_COMMITTER_*) into
	// the subprocess sandbox so raw git commits in bash tool calls are
	// attributed to the user, not just commits made through the git tools.
	setupGitIdentitySandboxEnv()

	switch args[1] {
	case "run":
		if err := preflightTools(); err != nil {
			_, _ = fmt.Fprintf(stderr, "preflight failed: %v\n", err)
			return 1
		}
		if err := runEntry(); err != nil {
			_, _ = fmt.Fprintf(stderr, "agent run failed: %v\n", err)
			return 1
		}
		return 0
	case "slack":
		if err := preflightTools(); err != nil {
			_, _ = fmt.Fprintf(stderr, "preflight failed: %v\n", err)
			return 1
		}
		if err := slackEntry(); err != nil {
			_, _ = fmt.Fprintf(stderr, "slack connector failed: %v\n", err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "Unknown subcommand: %s\nUsage: agent <run|slack>\n", args[1])
		return 1
	}
}

func main() {
	os.Exit(runCLI(os.Args, os.Stderr))
}
