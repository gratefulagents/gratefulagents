package dashboard

import (
	"testing"
)

func TestParseRuntimeImageCatalogValid(t *testing.T) {
	raw := []byte(`
images:
  - id: ruby
    label: Ruby
    description: Official Ruby image
    versions:
      - version: "3.4"
        image: docker.io/library/ruby:3.4
        default: true
      - version: "3.3"
        image: docker.io/library/ruby:3.3
  - id: default
    label: Default
    default: true
    versions:
      - version: latest
        image: ""
`)
	options, err := parseRuntimeImageCatalog(raw)
	if err != nil {
		t.Fatalf("parseRuntimeImageCatalog: %v", err)
	}
	if len(options) != 2 {
		t.Fatalf("expected 2 options, got %d", len(options))
	}
	ruby := options[0]
	if ruby.Id != "ruby" || len(ruby.Versions) != 2 {
		t.Fatalf("unexpected ruby option: %+v", ruby)
	}
	if !ruby.Versions[0].IsDefault || ruby.Versions[0].Image != "docker.io/library/ruby:3.4" {
		t.Fatalf("ruby default version mishandled: %+v", ruby.Versions[0])
	}
	if ruby.Versions[1].IsDefault {
		t.Fatalf("ruby 3.3 must not be default")
	}
	if ruby.IsDefault {
		t.Fatalf("ruby should not be the default language")
	}
	def := options[1]
	if !def.IsDefault || len(def.Versions) != 1 || def.Versions[0].Image != "" || !def.Versions[0].IsDefault {
		t.Fatalf("default entry mishandled: %+v", def)
	}
}

func TestParseRuntimeImageCatalogSkipsBadEntriesAndDupes(t *testing.T) {
	raw := []byte(`
images:
  - id: ""
    label: Nameless
    versions:
      - version: "1"
        image: img:1
  - id: noversions
    label: No versions
  - id: go
    label: Go
    versions:
      - version: "1.26"
        image: docker.io/library/golang:1.26
      - version: ""
        image: skipped:1
      - version: "1.26"
        image: duplicate:1
  - id: go
    label: Go duplicate
    versions:
      - version: "1.25"
        image: docker.io/library/golang:1.25
`)
	options, err := parseRuntimeImageCatalog(raw)
	if err != nil {
		t.Fatalf("parseRuntimeImageCatalog: %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("expected 1 option, got %d: %+v", len(options), options)
	}
	got := options[0]
	if got.Id != "go" || len(got.Versions) != 1 || got.Versions[0].Image != "docker.io/library/golang:1.26" {
		t.Fatalf("duplicate/blank version handling wrong: %+v", got)
	}
	if !got.IsDefault || !got.Versions[0].IsDefault {
		t.Fatalf("sole entry/version should become default when none flagged: %+v", got)
	}
}

func TestParseRuntimeImageCatalogFirstDefaultWins(t *testing.T) {
	raw := []byte(`
images:
  - id: a
    label: A
    default: true
    versions:
      - version: "1"
        image: a:1
        default: true
      - version: "2"
        image: a:2
        default: true
  - id: b
    label: B
    default: true
    versions:
      - version: "1"
        image: b:1
`)
	options, err := parseRuntimeImageCatalog(raw)
	if err != nil {
		t.Fatalf("parseRuntimeImageCatalog: %v", err)
	}
	if !options[0].IsDefault || options[1].IsDefault {
		t.Fatalf("expected only first language default to win: %+v %+v", options[0], options[1])
	}
	if !options[0].Versions[0].IsDefault || options[0].Versions[1].IsDefault {
		t.Fatalf("expected only first version default to win: %+v", options[0].Versions)
	}
}

func TestParseRuntimeImageCatalogRejectsEmptyAndInvalid(t *testing.T) {
	if _, err := parseRuntimeImageCatalog(nil); err == nil {
		t.Fatal("empty input should error")
	}
	if _, err := parseRuntimeImageCatalog([]byte("images: [")); err == nil {
		t.Fatal("malformed yaml should error")
	}
	if _, err := parseRuntimeImageCatalog([]byte("images: []")); err == nil {
		t.Fatal("no entries should error")
	}
	if _, err := parseRuntimeImageCatalog([]byte("unknownField: 1")); err == nil {
		t.Fatal("unknown fields should error under strict parsing")
	}
	// Entries whose versions are all invalid must not survive.
	raw := []byte(`
images:
  - id: broken
    label: Broken
    versions:
      - version: ""
        image: x:1
`)
	if _, err := parseRuntimeImageCatalog(raw); err == nil {
		t.Fatal("catalog with only unusable entries should error")
	}
}

func TestBuiltinRuntimeImageCatalogShape(t *testing.T) {
	options := builtinRuntimeImageCatalog()
	if len(options) == 0 {
		t.Fatal("builtin catalog must not be empty")
	}
	languageDefaults := 0
	seen := map[string]struct{}{}
	for _, opt := range options {
		if opt.Id == "" || opt.Label == "" {
			t.Fatalf("builtin entry missing id/label: %+v", opt)
		}
		if _, dup := seen[opt.Id]; dup {
			t.Fatalf("duplicate builtin id %q", opt.Id)
		}
		seen[opt.Id] = struct{}{}
		if len(opt.Versions) == 0 {
			t.Fatalf("builtin entry %q has no versions", opt.Id)
		}
		versionDefaults := 0
		versionsSeen := map[string]struct{}{}
		for _, v := range opt.Versions {
			if v.Version == "" {
				t.Fatalf("builtin entry %q has a version without a label", opt.Id)
			}
			if _, dup := versionsSeen[v.Version]; dup {
				t.Fatalf("builtin entry %q has duplicate version %q", opt.Id, v.Version)
			}
			versionsSeen[v.Version] = struct{}{}
			if v.IsDefault {
				versionDefaults++
			}
			if v.Image == "" && opt.Id != "default" {
				t.Fatalf("builtin entry %q version %q has empty image but is not the operator default", opt.Id, v.Version)
			}
		}
		if versionDefaults != 1 {
			t.Fatalf("builtin entry %q must have exactly one default version, got %d", opt.Id, versionDefaults)
		}
		if opt.IsDefault {
			languageDefaults++
			if opt.Versions[0].Image != "" {
				t.Fatalf("builtin default language should map to operator default image, got %q", opt.Versions[0].Image)
			}
		}
	}
	if languageDefaults != 1 {
		t.Fatalf("builtin catalog must have exactly one default language, got %d", languageDefaults)
	}
}
