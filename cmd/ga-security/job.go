// ga-security job runs one staged security-tool execution inside a
// short-lived Kubernetes Job. Everything comes from the environment: the Job
// downloads and verifies its target, runs the same typed runner the CLI uses,
// and republishes result.json plus raw artifacts through object storage. The
// controller reads the verdict from the uploaded manifest, so a scanner
// verdict of error or timeout is still a successful Job.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolrun"
	"github.com/gratefulagents/gratefulagents/internal/store/contentblob"
)

// maxTargetObjectBytes bounds the staged archive independently of the smaller
// project-content limit enforced by contentblob.S3.Get.
const maxTargetObjectBytes = 512 << 20

var sha256DigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// stagedDirectoryMediaTypes mirrors the media types
// securitytoolpacks.Registry.BuildInvocation demands from tools that scan a
// project tree. A staged target arrives as an archive and is extracted into a
// directory before the runner sees it, so the archive media type recorded in
// the spec would make those tools unrunnable.
type stagedTargetContract struct {
	mediaType  string
	singleFile bool
}

var stagedTargetContracts = map[string]map[string]stagedTargetContract{
	"aderyn": {
		"solidity_project": {mediaType: "application/vnd.gratefulagents.solidity-project.v1+directory"},
	},
	"forge-security-tests": {
		"foundry_project": {mediaType: "application/vnd.gratefulagents.foundry-security-project.v1+directory"},
	},
	"echidna": {
		"solidity_project": {mediaType: "application/vnd.gratefulagents.solidity-project.v1+directory"},
	},
	"mythril": {
		"solidity_contract": {mediaType: "application/vnd.gratefulagents.solidity-contract.v1+source", singleFile: true},
		"evm_bytecode":      {mediaType: "application/vnd.gratefulagents.evm-bytecode.v1+hex", singleFile: true},
	},
	"slither": {
		"solidity_project": {mediaType: "application/vnd.gratefulagents.solidity-project.v1+directory"},
	},
	"halmos": {
		"foundry_project": {mediaType: "application/vnd.gratefulagents.foundry-security-project.v1+directory"},
	},
}

// objectStore is the slice of blob storage the Job needs; the fake in the
// tests implements it so no test touches S3.
type objectStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, content []byte, mediaType string) error
}

type jobDeps struct {
	env    func(string) string
	store  objectStore
	run    func(context.Context, securitytoolpacks.RunConfig) (securitytoolpacks.Result, error)
	stdout io.Writer
	stderr io.Writer
}

type jobSettings struct {
	configPath   string
	workdir      string
	targetKey    string
	targetDigest string
	outputPrefix string
}

func runJob(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "ga-security job takes no arguments; configuration comes from the environment")
		return 2
	}
	store, err := newS3JobStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ga-security job: object storage: %v\n", err)
		return 2
	}
	return executeJob(context.Background(), jobDeps{
		env:    os.Getenv,
		store:  store,
		run:    runPinnedRegistry,
		stdout: os.Stdout,
		stderr: os.Stderr,
	})
}

// executeJob exits non-zero only for staging or infrastructure failures. Once
// the manifest is uploaded the Job succeeded, whatever the scanner concluded.
func executeJob(ctx context.Context, deps jobDeps) int {
	manifest, err := stageRunAndPublish(ctx, deps)
	if err != nil {
		_, _ = fmt.Fprintf(deps.stderr, "ga-security job: %v\n", err)
		return 2
	}
	encoder := json.NewEncoder(deps.stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		_, _ = fmt.Fprintf(deps.stderr, "ga-security job: encode manifest: %v\n", err)
		return 2
	}
	return 0
}

func stageRunAndPublish(ctx context.Context, deps jobDeps) (securitytoolrun.Manifest, error) {
	settings, err := jobSettingsFromEnv(deps.env)
	if err != nil {
		return securitytoolrun.Manifest{}, err
	}
	config, err := readRunConfig(settings.configPath)
	if err != nil {
		return securitytoolrun.Manifest{}, err
	}
	if settings.targetKey != "" {
		locator, err := stageTarget(ctx, deps.store, settings)
		if err != nil {
			return securitytoolrun.Manifest{}, err
		}
		contract := stagedTargetContracts[config.Tool][config.Target.Type]
		if contract.singleFile {
			locator, err = stagedSingleFile(locator)
			if err != nil {
				return securitytoolrun.Manifest{}, err
			}
		}
		config.Target.Locator = locator
		if contract.mediaType != "" {
			config.Target.MediaType = contract.mediaType
		}
		// The control plane authenticates the staged tarball. After that
		// verified archive is extracted, the runner authenticates the exact
		// file or directory representation it executes, not the tar encoding.
		extractedDigest, exists, err := securitytoolpacks.DigestPath(locator)
		if err != nil {
			return securitytoolrun.Manifest{}, fmt.Errorf("digest extracted target: %w", err)
		}
		if !exists {
			return securitytoolrun.Manifest{}, errors.New("extracted target is unavailable")
		}
		config.Target.Digest = extractedDigest
	}

	result, err := deps.run(ctx, config)
	if err != nil {
		return securitytoolrun.Manifest{}, err
	}
	outputDir := filepath.Join(settings.workdir, "out")
	if err := persist(outputDir, result); err != nil {
		return securitytoolrun.Manifest{}, fmt.Errorf("persist result: %w", err)
	}
	return publishResult(ctx, deps.store, settings.outputPrefix, outputDir, config.Tool, result)
}

func jobSettingsFromEnv(env func(string) string) (jobSettings, error) {
	settings := jobSettings{
		configPath:   valueOrDefault(env(securitytoolrun.EnvConfig), securitytoolrun.ConfigPath),
		workdir:      valueOrDefault(env(securitytoolrun.EnvWorkdir), securitytoolrun.WorkDir),
		targetKey:    strings.TrimSpace(env(securitytoolrun.EnvTargetKey)),
		targetDigest: strings.TrimSpace(env(securitytoolrun.EnvTargetDigest)),
		outputPrefix: strings.Trim(strings.TrimSpace(env(securitytoolrun.EnvOutputPrefix)), "/"),
	}
	if settings.outputPrefix == "" {
		return jobSettings{}, fmt.Errorf("%s is required", securitytoolrun.EnvOutputPrefix)
	}
	if settings.targetKey != "" && !sha256DigestPattern.MatchString(settings.targetDigest) {
		return jobSettings{}, fmt.Errorf("%s must be sha256:<hex> when %s is set, got %q",
			securitytoolrun.EnvTargetDigest, securitytoolrun.EnvTargetKey, settings.targetDigest)
	}
	return settings, nil
}

func readRunConfig(path string) (securitytoolpacks.RunConfig, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return securitytoolpacks.RunConfig{}, fmt.Errorf("read config: %w", err)
	}
	var config securitytoolpacks.RunConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return securitytoolpacks.RunConfig{}, fmt.Errorf("invalid config: %w", err)
	}
	return config, nil
}

// stageTarget downloads the staged archive, refuses to run the scanner unless
// its bytes match the recorded digest, and extracts it into <workdir>/target.
func stageTarget(ctx context.Context, store objectStore, settings jobSettings) (string, error) {
	archive, err := store.Get(ctx, settings.targetKey)
	if err != nil {
		return "", fmt.Errorf("download target %q: %w", settings.targetKey, err)
	}
	if actual := digestBytes(archive); actual != settings.targetDigest {
		return "", fmt.Errorf("target %q digest mismatch: recorded %s, downloaded %s",
			settings.targetKey, settings.targetDigest, actual)
	}
	targetDir := filepath.Join(settings.workdir, "target")
	if err := extractTarGz(archive, targetDir, defaultTarLimits); err != nil {
		return "", fmt.Errorf("extract target %q: %w", settings.targetKey, err)
	}
	return targetDir, nil
}

func stagedSingleFile(targetDir string) (string, error) {
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return "", fmt.Errorf("read staged single-file target: %w", err)
	}
	if len(entries) != 1 || !entries[0].Type().IsRegular() {
		return "", errors.New("staged single-file target must contain exactly one regular file")
	}
	return filepath.Join(targetDir, entries[0].Name()), nil
}

func publishResult(ctx context.Context, store objectStore, prefix, outputDir, tool string,
	result securitytoolpacks.Result,
) (securitytoolrun.Manifest, error) {
	resultKey := prefix + "/" + securitytoolrun.ResultObjectName
	resultBytes, err := os.ReadFile(filepath.Join(outputDir, securitytoolrun.ResultObjectName))
	if err != nil {
		return securitytoolrun.Manifest{}, fmt.Errorf("read result.json: %w", err)
	}
	if err := store.Put(ctx, resultKey, resultBytes, "application/json"); err != nil {
		return securitytoolrun.Manifest{}, fmt.Errorf("upload %s: %w", resultKey, err)
	}

	manifest := securitytoolrun.Manifest{
		SchemaVersion:   securitytoolrun.ManifestSchemaVersion,
		Tool:            tool,
		Status:          string(result.Status),
		FindingCount:    len(result.Findings),
		ResultObjectKey: resultKey,
		ResultDigest:    digestBytes(resultBytes),
		Errors:          append([]string(nil), result.Errors...),
	}
	for _, artifact := range result.Artifacts {
		key := prefix + "/" + artifact.Name
		data, err := os.ReadFile(filepath.Join(outputDir, artifact.Name))
		if err != nil {
			return securitytoolrun.Manifest{}, fmt.Errorf("read artifact %s: %w", artifact.Name, err)
		}
		if err := store.Put(ctx, key, data, artifact.MediaType); err != nil {
			return securitytoolrun.Manifest{}, fmt.Errorf("upload %s: %w", key, err)
		}
		manifest.Artifacts = append(manifest.Artifacts, securitytoolrun.ManifestArtifact{
			Name:      artifact.Name,
			MediaType: artifact.MediaType,
			Digest:    artifact.Digest,
			Size:      int64(artifact.Size),
			ObjectKey: key,
		})
	}

	if err := manifest.Validate(); err != nil {
		return securitytoolrun.Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return securitytoolrun.Manifest{}, fmt.Errorf("encode manifest: %w", err)
	}
	manifestKey := prefix + "/" + securitytoolrun.ManifestObjectName
	if err := store.Put(ctx, manifestKey, manifestBytes, "application/json"); err != nil {
		return securitytoolrun.Manifest{}, fmt.Errorf("upload %s: %w", manifestKey, err)
	}
	return manifest, nil
}

func runPinnedRegistry(ctx context.Context, config securitytoolpacks.RunConfig) (securitytoolpacks.Result, error) {
	imageDigest, err := executableDigest()
	if err != nil {
		return securitytoolpacks.Result{}, fmt.Errorf("hash ga-security executable: %w", err)
	}
	registry, err := securitytoolpacks.NewRegistry(securitytoolpacks.DefaultManifest(imageDigest, nil))
	if err != nil {
		return securitytoolpacks.Result{}, fmt.Errorf("invalid pinned registry: %w", err)
	}
	return securitytoolpacks.NewRunner(registry, securitytoolpacks.ProcessSandbox{}).Run(ctx, config), nil
}

type tarLimits struct {
	maxEntries    int
	maxEntryBytes int64
	maxTotalBytes int64
}

var defaultTarLimits = tarLimits{maxEntries: 200_000, maxEntryBytes: 512 << 20, maxTotalBytes: 2 << 30}

// extractTarGz unpacks a staged archive under dest. Entries that would leave
// the destination directory, traverse a symlink, or blow the size budget abort
// the whole extraction: a staged target is untrusted input.
func extractTarGz(archive []byte, dest string, limits tarLimits) error {
	stream, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() { _ = stream.Close() }()
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}

	reader := tar.NewReader(stream)
	var entries int
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive entry: %w", err)
		}
		entries++
		if entries > limits.maxEntries {
			return fmt.Errorf("archive holds more than %d entries", limits.maxEntries)
		}
		path, err := resolveEntryPath(root, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureDir(root, path); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size > limits.maxEntryBytes {
				return fmt.Errorf("archive entry %q is %d bytes, over the %d-byte entry limit",
					header.Name, header.Size, limits.maxEntryBytes)
			}
			if err := ensureDir(root, filepath.Dir(path)); err != nil {
				return err
			}
			written, err := writeEntry(path, reader, limits.maxTotalBytes-total)
			if err != nil {
				return fmt.Errorf("archive entry %q: %w", header.Name, err)
			}
			total += written
		case tar.TypeSymlink:
			if err := ensureDir(root, filepath.Dir(path)); err != nil {
				return err
			}
			if err := checkLinkTarget(root, filepath.Dir(path), header.Linkname, header.Name); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, path); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := ensureDir(root, filepath.Dir(path)); err != nil {
				return err
			}
			if err := checkLinkTarget(root, root, header.Linkname, header.Name); err != nil {
				return err
			}
			if err := os.Link(filepath.Join(root, filepath.FromSlash(header.Linkname)), path); err != nil {
				return err
			}
		default:
			// devices, fifos, and other special entries are never extracted.
		}
	}
}

func resolveEntryPath(root, name string) (string, error) {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return "", errors.New("archive entry has an empty name")
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") || filepath.VolumeName(clean) != "" {
		return "", fmt.Errorf("archive entry %q uses an absolute path", name)
	}
	if slices.Contains(strings.Split(filepath.ToSlash(clean), "/"), "..") {
		return "", fmt.Errorf("archive entry %q traverses outside the extraction root", name)
	}
	path := filepath.Join(root, filepath.FromSlash(clean))
	if !withinRoot(root, path) {
		return "", fmt.Errorf("archive entry %q traverses outside the extraction root", name)
	}
	return path, nil
}

func checkLinkTarget(root, base, linkname, entry string) error {
	if strings.TrimSpace(linkname) == "" {
		return fmt.Errorf("archive entry %q has an empty link target", entry)
	}
	if filepath.IsAbs(linkname) || strings.HasPrefix(linkname, "/") {
		return fmt.Errorf("archive entry %q links to absolute path %q", entry, linkname)
	}
	if !withinRoot(root, filepath.Join(base, filepath.FromSlash(linkname))) {
		return fmt.Errorf("archive entry %q links to %q outside the extraction root", entry, linkname)
	}
	return nil
}

func withinRoot(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

// ensureDir creates dir one component at a time so an already-extracted
// symlink can never become part of a later entry's path.
func ensureDir(root, dir string) error {
	relative, err := filepath.Rel(root, dir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("archive path %q is outside the extraction root", dir)
	}
	current := root
	if relative == "." {
		return nil
	}
	for part := range strings.SplitSeq(relative, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
		case err != nil:
			return err
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("archive path %q traverses a symlink", current)
		case !info.IsDir():
			return fmt.Errorf("archive path %q is not a directory", current)
		}
	}
	return nil
}

func writeEntry(path string, reader io.Reader, remaining int64) (int64, error) {
	if remaining <= 0 {
		return 0, errors.New("archive exceeds the extraction size limit")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(file, io.LimitReader(reader, remaining+1))
	closeErr := file.Close()
	if copyErr != nil {
		return written, copyErr
	}
	if closeErr != nil {
		return written, closeErr
	}
	if written > remaining {
		return written, errors.New("archive exceeds the extraction size limit")
	}
	return written, nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func valueOrDefault(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

// s3JobStore uploads through the shared content-blob store and reads staged
// target archives with its own client, because contentblob.S3.Get caps objects
// at the much smaller project-content limit.
type s3JobStore struct {
	blobs  *contentblob.S3
	client *s3.Client
	bucket string
}

func newS3JobStore() (*s3JobStore, error) {
	blobs, err := contentblob.NewS3FromEnv()
	if err != nil {
		return nil, err
	}
	region := valueOrDefault(os.Getenv("S3_REGION"), "us-east-1")
	cfg := aws.Config{Region: region}
	accessKeyID := strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID"))
	secretAccessKey := strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY"))
	if accessKeyID != "" {
		cfg.Credentials = credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")
	}
	endpoint := strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	if endpoint != "" {
		parsed, err := url.ParseRequestURI(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, errors.New("S3_ENDPOINT must be an absolute URL")
		}
	}
	client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
			options.UsePathStyle = true
		}
	})
	return &s3JobStore{blobs: blobs, client: client, bucket: strings.TrimSpace(os.Getenv("S3_BUCKET"))}, nil
}

func (s *s3JobStore) Put(ctx context.Context, key string, content []byte, mediaType string) error {
	return s.blobs.Put(ctx, key, content, mediaType)
}

func (s *s3JobStore) Get(ctx context.Context, key string) ([]byte, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("getting object %q from S3 bucket %q: %w", key, s.bucket, err)
	}
	if output.ContentLength != nil && *output.ContentLength > maxTargetObjectBytes {
		_ = output.Body.Close()
		return nil, fmt.Errorf("object %q exceeds the %d-byte limit", key, int64(maxTargetObjectBytes))
	}
	body, readErr := io.ReadAll(io.LimitReader(output.Body, maxTargetObjectBytes+1))
	closeErr := output.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("reading object %q: %w", key, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("closing object %q: %w", key, closeErr)
	}
	if len(body) > maxTargetObjectBytes {
		return nil, fmt.Errorf("object %q exceeds the %d-byte limit", key, int64(maxTargetObjectBytes))
	}
	return body, nil
}

var _ objectStore = (*s3JobStore)(nil)
