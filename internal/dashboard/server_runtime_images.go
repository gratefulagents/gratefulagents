package dashboard

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	// runtimeImageCatalogConfigMapEnv names the ConfigMap (in the manager
	// namespace) that overrides the built-in worker image catalog.
	runtimeImageCatalogConfigMapEnv     = "RUNTIME_IMAGE_CATALOG_CONFIGMAP"
	defaultRuntimeImageCatalogConfigMap = "runtime-image-catalog"
	runtimeImageCatalogKey              = "catalog.yaml"
)

// builtinRuntimeImageCatalog is the curated language → image catalog served
// when no admin-provided ConfigMap overrides it. Only official glibc-based
// (Debian/Ubuntu) images: the injected agent toolkit does not support
// musl/alpine variants. Version defaults track the newest stable/LTS line.
func builtinRuntimeImageCatalog() []*platform.RuntimeImageOption {
	return []*platform.RuntimeImageOption{
		{Id: "default", Label: "Default (multi-language)", Description: "Operator's batteries-included worker image (Go, Node, Python, Elixir, …)", IsDefault: true, Versions: []*platform.RuntimeImageVersion{
			{Version: "latest", Image: "", IsDefault: true},
		}},
		{Id: "ruby", Label: "Ruby", Description: "Official Ruby image (Debian)", Versions: []*platform.RuntimeImageVersion{
			{Version: "3.4", Image: "docker.io/library/ruby:3.4", IsDefault: true},
			{Version: "3.3", Image: "docker.io/library/ruby:3.3"},
			{Version: "3.2", Image: "docker.io/library/ruby:3.2"},
		}},
		{Id: "go", Label: "Go", Description: "Official Go image (Debian)", Versions: []*platform.RuntimeImageVersion{
			{Version: "1.26", Image: "docker.io/library/golang:1.26", IsDefault: true},
			{Version: "1.25", Image: "docker.io/library/golang:1.25"},
		}},
		{Id: "node", Label: "Node.js", Description: "Official Node.js image (Debian)", Versions: []*platform.RuntimeImageVersion{
			{Version: "24", Image: "docker.io/library/node:24", IsDefault: true},
			{Version: "22", Image: "docker.io/library/node:22"},
			{Version: "20", Image: "docker.io/library/node:20"},
		}},
		{Id: "python", Label: "Python", Description: "Official Python image (Debian)", Versions: []*platform.RuntimeImageVersion{
			{Version: "3.14", Image: "docker.io/library/python:3.14", IsDefault: true},
			{Version: "3.13", Image: "docker.io/library/python:3.13"},
			{Version: "3.12", Image: "docker.io/library/python:3.12"},
		}},
		{Id: "java", Label: "Java", Description: "Eclipse Temurin JDK image (Ubuntu)", Versions: []*platform.RuntimeImageVersion{
			{Version: "25", Image: "docker.io/library/eclipse-temurin:25", IsDefault: true},
			{Version: "21", Image: "docker.io/library/eclipse-temurin:21"},
			{Version: "17", Image: "docker.io/library/eclipse-temurin:17"},
		}},
		{Id: "rust", Label: "Rust", Description: "Official Rust image (Debian)", Versions: []*platform.RuntimeImageVersion{
			{Version: "1", Image: "docker.io/library/rust:1", IsDefault: true},
		}},
		{Id: "php", Label: "PHP", Description: "Official PHP CLI image (Debian)", Versions: []*platform.RuntimeImageVersion{
			{Version: "8.4", Image: "docker.io/library/php:8.4-cli", IsDefault: true},
			{Version: "8.3", Image: "docker.io/library/php:8.3-cli"},
			{Version: "8.2", Image: "docker.io/library/php:8.2-cli"},
		}},
		{Id: "elixir", Label: "Elixir", Description: "Official Elixir image (Debian)", Versions: []*platform.RuntimeImageVersion{
			{Version: "1.18", Image: "docker.io/library/elixir:1.18", IsDefault: true},
			{Version: "1.17", Image: "docker.io/library/elixir:1.17"},
		}},
	}
}

// runtimeImageCatalogDoc is the catalog.yaml schema of the override ConfigMap.
type runtimeImageCatalogDoc struct {
	Images []runtimeImageCatalogEntry `json:"images"`
}

type runtimeImageCatalogEntry struct {
	ID          string                       `json:"id"`
	Label       string                       `json:"label"`
	Description string                       `json:"description"`
	Default     bool                         `json:"default"`
	Versions    []runtimeImageCatalogVersion `json:"versions"`
}

type runtimeImageCatalogVersion struct {
	Version string `json:"version"`
	Image   string `json:"image"`
	Default bool   `json:"default"`
}

// ListRuntimeImages serves the worker image catalog for project/agent/run
// pickers: the admin ConfigMap when present and valid, else the built-in set.
func (s *Server) ListRuntimeImages(ctx context.Context, _ *platform.ListRuntimeImagesRequest) (*platform.ListRuntimeImagesResponse, error) {
	return &platform.ListRuntimeImagesResponse{Images: s.runtimeImageCatalog(ctx)}, nil
}

func (s *Server) runtimeImageCatalog(ctx context.Context) []*platform.RuntimeImageOption {
	namespace := strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
	if namespace == "" || s.k8sClient == nil {
		return builtinRuntimeImageCatalog()
	}
	name := strings.TrimSpace(os.Getenv(runtimeImageCatalogConfigMapEnv))
	if name == "" {
		name = defaultRuntimeImageCatalogConfigMap
	}
	cm := &corev1.ConfigMap{}
	if err := s.k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, cm); err != nil {
		// Absent ConfigMap is the normal "no override" case; other errors are
		// worth a warning but never break the picker.
		if !apierrors.IsNotFound(err) {
			log.Printf("WARN: reading runtime image catalog ConfigMap %s/%s: %v", namespace, name, err)
		}
		return builtinRuntimeImageCatalog()
	}
	options, err := parseRuntimeImageCatalog([]byte(cm.Data[runtimeImageCatalogKey]))
	if err != nil {
		log.Printf("WARN: invalid runtime image catalog in ConfigMap %s/%s: %v; using built-in catalog", namespace, name, err)
		return builtinRuntimeImageCatalog()
	}
	return options
}

// parseRuntimeImageCatalog converts catalog.yaml bytes into catalog options,
// skipping malformed entries. It returns an error when the document does not
// parse or yields no usable entries.
func parseRuntimeImageCatalog(raw []byte) ([]*platform.RuntimeImageOption, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, errEmptyRuntimeImageCatalog
	}
	var doc runtimeImageCatalogDoc
	if err := yaml.UnmarshalStrict(raw, &doc); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	options := make([]*platform.RuntimeImageOption, 0, len(doc.Images))
	defaultSeen := false
	for _, entry := range doc.Images {
		id := strings.TrimSpace(entry.ID)
		label := strings.TrimSpace(entry.Label)
		if id == "" || label == "" {
			log.Printf("WARN: runtime image catalog entry missing id/label skipped (id=%q label=%q)", entry.ID, entry.Label)
			continue
		}
		if _, dup := seen[id]; dup {
			log.Printf("WARN: duplicate runtime image catalog id %q skipped", id)
			continue
		}
		versions := parseRuntimeImageCatalogVersions(id, entry.Versions)
		if len(versions) == 0 {
			log.Printf("WARN: runtime image catalog entry %q has no usable versions, skipped", id)
			continue
		}
		seen[id] = struct{}{}
		isDefault := entry.Default && !defaultSeen
		defaultSeen = defaultSeen || isDefault
		options = append(options, &platform.RuntimeImageOption{
			Id:          id,
			Label:       label,
			Description: strings.TrimSpace(entry.Description),
			IsDefault:   isDefault,
			Versions:    versions,
		})
	}
	if len(options) == 0 {
		return nil, errEmptyRuntimeImageCatalog
	}
	if !defaultSeen {
		options[0].IsDefault = true
	}
	return options, nil
}

// parseRuntimeImageCatalogVersions validates one entry's version list: version
// labels are required and unique within the entry, and exactly one version is
// the default (first flagged wins, else the first version).
func parseRuntimeImageCatalogVersions(entryID string, versions []runtimeImageCatalogVersion) []*platform.RuntimeImageVersion {
	seen := map[string]struct{}{}
	out := make([]*platform.RuntimeImageVersion, 0, len(versions))
	defaultSeen := false
	for _, v := range versions {
		version := strings.TrimSpace(v.Version)
		if version == "" {
			log.Printf("WARN: runtime image catalog entry %q version without a label skipped", entryID)
			continue
		}
		if _, dup := seen[version]; dup {
			log.Printf("WARN: duplicate version %q in runtime image catalog entry %q skipped", version, entryID)
			continue
		}
		seen[version] = struct{}{}
		isDefault := v.Default && !defaultSeen
		defaultSeen = defaultSeen || isDefault
		out = append(out, &platform.RuntimeImageVersion{
			Version:   version,
			Image:     strings.TrimSpace(v.Image),
			IsDefault: isDefault,
		})
	}
	if len(out) > 0 && !defaultSeen {
		out[0].IsDefault = true
	}
	return out
}

var errEmptyRuntimeImageCatalog = errors.New("runtime image catalog has no usable entries")
