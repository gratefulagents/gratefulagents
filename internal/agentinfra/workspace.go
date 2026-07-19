package agentinfra

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// SetupGHAuth configures gh CLI auth and sets it as the git credential helper.
func SetupGHAuth(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		log.Println("WARN: no GitHub token provided; skipping gh auth setup")
		return nil
	}

	cmd := exec.Command("gh", "auth", "login", "--with-token")
	cmd.Stdin = strings.NewReader(token)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh auth login: %w", err)
	}

	cmd = exec.Command("gh", "auth", "setup-git")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh auth setup-git: %w", err)
	}

	log.Println("gh auth configured.")
	return nil
}

// GitExec runs a git command, optionally in a specific directory.
func GitExec(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
