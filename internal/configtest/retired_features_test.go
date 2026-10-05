package configtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestRetiredFeatureSurfacesAbsent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for kind := range scheme.AllKnownTypes() {
		if strings.HasPrefix(kind.Kind, "Security") {
			t.Errorf("retired kind still registered: %s", kind)
		}
	}
	methods := platform.File_rpc_platform_service_proto.Services().ByName("PlatformService").Methods()
	for i := 0; i < methods.Len(); i++ {
		name := string(methods.Get(i).Name())
		if strings.Contains(name, "Security") || strings.Contains(name, "BugReport") {
			t.Errorf("retired RPC still advertised: %s", name)
		}
	}
	root := filepath.Join("..", "..")
	for _, directory := range []string{"configs", "dist/chart/files/bootstrap"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".yaml") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, retired := range []string{"report_bug", "report_platform_bug", "platform.gratefulagents.dev/security-skill:", "kind: Security"} {
				if strings.Contains(string(data), retired) {
					t.Errorf("%s advertises retired surface %s", path, retired)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
