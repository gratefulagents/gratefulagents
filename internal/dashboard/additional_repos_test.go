package dashboard

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeAdditionalRepoURLs(t *testing.T) {
	tests := []struct {
		name    string
		urls    []string
		primary string
		want    []string
		wantErr string
	}{
		{
			name: "empty list",
			urls: nil,
			want: nil,
		},
		{
			name: "valid https and ssh URLs",
			urls: []string{"https://github.com/example/lib.git", "git@github.com:example/tools.git"},
			want: []string{"https://github.com/example/lib.git", "git@github.com:example/tools.git"},
		},
		{
			name:    "trims whitespace and drops empties, duplicates, and the primary",
			urls:    []string{" https://github.com/example/lib.git ", "", "https://github.com/example/lib.git", "https://github.com/example/app.git"},
			primary: "https://github.com/example/app.git",
			want:    []string{"https://github.com/example/lib.git"},
		},
		{
			name:    "rejects unsupported transport",
			urls:    []string{"ftp://example.com/lib.git"},
			wantErr: "must be an http(s), ssh, or git URL",
		},
		{
			name:    "rejects the reserved primary clone directory name",
			urls:    []string{"https://github.com/example/repo.git"},
			wantErr: "reserved",
		},
		{
			name:    "rejects clone directory name collisions",
			urls:    []string{"https://github.com/alpha/lib.git", "https://github.com/beta/lib.git"},
			wantErr: "would both clone",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeAdditionalRepoURLs(tt.urls, tt.primary)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("normalizeAdditionalRepoURLs() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeAdditionalRepoURLs() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("normalizeAdditionalRepoURLs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
