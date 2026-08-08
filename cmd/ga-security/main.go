// ga-security executes one deterministic security-tool registry request.
// Agents may invoke this binary through Bash, but cannot choose the underlying
// executable or append arbitrary arguments: both come from the compiled
// registry after typed validation.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
)

const (
	imageDigestEnv   = "GRATEFULAGENTS_SECURITY_TOOLS_IMAGE_DIGEST"
	knowledgePinsEnv = "GRATEFULAGENTS_SECURITY_KNOWLEDGE_PINS"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	flags := flag.NewFlagSet("ga-security", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to typed RunConfig JSON")
	outputDir := flags.String("output", "", "directory for result and raw artifacts")
	showManifest := flags.Bool("manifest", false, "print the pinned registry manifest and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	imageDigest := strings.TrimSpace(os.Getenv(imageDigestEnv))
	if imageDigest == "" {
		var err error
		imageDigest, err = executableDigest()
		if err != nil {
			fmt.Fprintf(os.Stderr, "hash ga-security executable: %v\n", err)
			return 2
		}
	}
	var knowledgePins map[string]string
	if raw := strings.TrimSpace(os.Getenv(knowledgePinsEnv)); raw != "" {
		if err := json.Unmarshal([]byte(raw), &knowledgePins); err != nil {
			fmt.Fprintf(os.Stderr, "%s must be a JSON object: %v\n", knowledgePinsEnv, err)
			return 2
		}
	}
	manifest := securitytoolpacks.DefaultManifest(imageDigest, knowledgePins)
	registry, err := securitytoolpacks.NewRegistry(manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid pinned registry: %v\n", err)
		return 2
	}
	if *showManifest {
		return writeJSON(os.Stdout, registry.Manifest())
	}
	if *configPath == "" || *outputDir == "" {
		fmt.Fprintln(os.Stderr, "--config and --output are required")
		return 2
	}
	configBytes, err := os.ReadFile(filepath.Clean(*configPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read config: %v\n", err)
		return 2
	}
	var config securitytoolpacks.RunConfig
	decoder := json.NewDecoder(strings.NewReader(string(configBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		return 2
	}

	result := securitytoolpacks.NewRunner(registry, securitytoolpacks.ProcessSandbox{}).Run(context.Background(), config)
	if err := persist(*outputDir, result); err != nil {
		fmt.Fprintf(os.Stderr, "persist result: %v\n", err)
		return 1
	}
	if writeJSON(os.Stdout, result) != 0 {
		return 1
	}
	return securitytoolpacks.ResultExitCode(result.Status)
}

func persist(dir string, result securitytoolpacks.Result) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("output directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for i := range result.Artifacts {
		artifact := &result.Artifacts[i]
		name := fmt.Sprintf("raw-%02d", i)
		if err := os.WriteFile(filepath.Join(dir, name), artifact.Data, 0o600); err != nil {
			return err
		}
		artifact.Name = name
		artifact.Data = nil
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, "result.json"), data, 0o600)
}

func executableDigest() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func writeJSON(file *os.File, value any) int {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "encode output: %v\n", err)
		return 1
	}
	return 0
}
