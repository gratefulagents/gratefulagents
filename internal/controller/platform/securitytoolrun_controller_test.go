/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolrun"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

const (
	securityToolTestDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	securityToolTestImage  = "ghcr.io/gratefulagents/security-tools@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

type stubBlobReader struct {
	objects map[string][]byte
	err     error
	calls   int
	deleted []string
}

func (s *stubBlobReader) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	delete(s.objects, key)
	return nil
}

func (s *stubBlobReader) Get(_ context.Context, key string) ([]byte, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("object %s not found", key)
	}
	return data, nil
}

func newSecurityToolRun(mutate func(*platformv1alpha1.SecurityToolRun)) *platformv1alpha1.SecurityToolRun {
	run := &platformv1alpha1.SecurityToolRun{
		ObjectMeta: metav1.ObjectMeta{Name: "scan", Namespace: "ns", UID: types.UID("run-uid"), Generation: 1},
		Spec: platformv1alpha1.SecurityToolRunSpec{
			Tool: "authorization-matrix",
			Target: platformv1alpha1.SecurityToolTarget{
				Type:            "authorization_matrix",
				Locator:         "matrix.json",
				Revision:        "1a2b3c",
				Digest:          securityToolTestDigest,
				StagedObjectKey: securitytoolrun.TargetObjectKey("ns", "scan"),
			},
		},
	}
	if mutate != nil {
		mutate(run)
	}
	return run
}

func newSecurityToolRunClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(client-go): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&platformv1alpha1.SecurityToolRun{}).
		Build()
}

func reconcileSecurityToolRun(t *testing.T, c client.Client, blobs SecurityToolBlobReader) *platformv1alpha1.SecurityToolRun {
	t.Helper()
	r := &SecurityToolRunReconciler{Client: c, Blobs: blobs}
	key := client.ObjectKey{Namespace: "ns", Name: "scan"}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	run := &platformv1alpha1.SecurityToolRun{}
	if err := c.Get(context.Background(), key, run); err != nil {
		t.Fatalf("Get(SecurityToolRun) error = %v", err)
	}
	return run
}

func getSecurityToolJob(t *testing.T, c client.Client) *batchv1.Job {
	t.Helper()
	job := &batchv1.Job{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "scan-job"}, job); err != nil {
		t.Fatalf("Get(Job) error = %v", err)
	}
	return job
}

func completeSecurityToolJob(t *testing.T, c client.Client, condition batchv1.JobConditionType, reason string) {
	t.Helper()
	job := getSecurityToolJob(t, c)
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type: condition, Status: corev1.ConditionTrue, Reason: reason,
	})
	if err := c.Status().Update(context.Background(), job); err != nil {
		t.Fatalf("Status().Update(Job) error = %v", err)
	}
}

func TestSecurityToolRunRejectsInvalidRequests(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	cases := []struct {
		name    string
		mutate  func(*platformv1alpha1.SecurityToolRun)
		message string
	}{
		{
			name:    "unknown tool",
			mutate:  func(run *platformv1alpha1.SecurityToolRun) { run.Spec.Tool = "definitely-not-a-tool" },
			message: `unknown registered tool "definitely-not-a-tool"`,
		},
		{
			name:    "wrong target type",
			mutate:  func(run *platformv1alpha1.SecurityToolRun) { run.Spec.Target.Type = "pcap" },
			message: `does not accept target type "pcap"`,
		},
		{
			name:    "missing digest",
			mutate:  func(run *platformv1alpha1.SecurityToolRun) { run.Spec.Target.Digest = "" },
			message: "immutable sha256 digest",
		},
		{
			name: "unknown argument",
			mutate: func(run *platformv1alpha1.SecurityToolRun) {
				run.Spec.Arguments = []platformv1alpha1.SecurityToolArgument{{Name: "--exec", Value: "sh"}}
			},
			message: `has no argument "--exec"`,
		},
		{
			name: "foreign staged object key",
			mutate: func(run *platformv1alpha1.SecurityToolRun) {
				run.Spec.Target.StagedObjectKey = "security-tool-runs/other/run/target.tar.gz"
			},
			message: "staged target must be",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newSecurityToolRunClient(t, newSecurityToolRun(tc.mutate))
			run := reconcileSecurityToolRun(t, c, &stubBlobReader{})
			if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseFailed {
				t.Fatalf("phase = %q, want Failed", run.Status.Phase)
			}
			if run.Status.Result == nil || run.Status.Result.Status != "error" {
				t.Fatalf("result = %+v, want status error", run.Status.Result)
			}
			if !strings.Contains(run.Status.Message, tc.message) {
				t.Fatalf("message = %q, want it to contain %q", run.Status.Message, tc.message)
			}
			if run.Status.ObservedGeneration != run.Generation {
				t.Fatalf("observedGeneration = %d, want %d", run.Status.ObservedGeneration, run.Generation)
			}
			job := &batchv1.Job{}
			err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "scan-job"}, job)
			if !apierrors.IsNotFound(err) {
				t.Fatalf("Get(Job) error = %v, want NotFound", err)
			}
		})
	}
}

func TestSecurityToolRunRequiresPinnedImage(t *testing.T) {
	t.Setenv(securityToolsImageEnv, "ghcr.io/gratefulagents/security-tools:latest")
	c := newSecurityToolRunClient(t, newSecurityToolRun(nil))
	run := reconcileSecurityToolRun(t, c, &stubBlobReader{})
	if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseFailed {
		t.Fatalf("phase = %q, want Failed", run.Status.Phase)
	}
	if run.Status.Result == nil || run.Status.Result.Status != "error" {
		t.Fatalf("result = %+v, want status error", run.Status.Result)
	}
	if !strings.Contains(run.Status.Message, "must be digest pinned") {
		t.Fatalf("message = %q", run.Status.Message)
	}

	t.Setenv(securityToolsAllowUnpinnedEnv, "true")
	c = newSecurityToolRunClient(t, newSecurityToolRun(nil))
	run = reconcileSecurityToolRun(t, c, &stubBlobReader{})
	if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseRunning {
		t.Fatalf("phase = %q, want Running with the override set", run.Status.Phase)
	}
	if job := getSecurityToolJob(t, c); job.Spec.Template.Spec.Containers[0].Image != "ghcr.io/gratefulagents/security-tools:latest" {
		t.Fatalf("image = %q", job.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestSecurityToolRunMissingImageFails(t *testing.T) {
	t.Setenv(securityToolsImageEnv, "")
	c := newSecurityToolRunClient(t, newSecurityToolRun(nil))
	run := reconcileSecurityToolRun(t, c, &stubBlobReader{})
	if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseFailed || !strings.Contains(run.Status.Message, "is not configured") {
		t.Fatalf("phase = %q message = %q", run.Status.Phase, run.Status.Message)
	}
}

//nolint:gocyclo // One hardened Job spec, asserted field by field.
func TestSecurityToolRunCreatesHardenedJob(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	t.Setenv("S3_BUCKET", "platform-bucket")
	c := newSecurityToolRunClient(t, newSecurityToolRun(nil))

	run := reconcileSecurityToolRun(t, c, &stubBlobReader{})
	if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseRunning {
		t.Fatalf("phase = %q, want Running", run.Status.Phase)
	}
	if run.Status.JobName != "scan-job" || run.Status.Image != securityToolTestImage || run.Status.StartedAt == nil {
		t.Fatalf("status = %+v", run.Status)
	}

	job := getSecurityToolJob(t, c)
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Fatalf("backoffLimit = %v, want 0", job.Spec.BackoffLimit)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 600 {
		t.Fatalf("ttlSecondsAfterFinished = %v, want 600", job.Spec.TTLSecondsAfterFinished)
	}
	tool, ok := mustSecurityToolRegistry(t).Tool("authorization-matrix")
	if !ok {
		t.Fatal("authorization-matrix is not registered")
	}
	wantDeadline := int64(tool.Budgets.Timeout.Seconds()) + 120
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != wantDeadline {
		t.Fatalf("activeDeadlineSeconds = %v, want %d", job.Spec.ActiveDeadlineSeconds, wantDeadline)
	}

	pod := job.Spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("restartPolicy = %q", pod.RestartPolicy)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatalf("automountServiceAccountToken = %v, want false", pod.AutomountServiceAccountToken)
	}
	container := pod.Containers[0]
	if got := append(append([]string{}, container.Command...), container.Args...); strings.Join(got, " ") != "ga-security job" {
		t.Fatalf("argv = %v, want [ga-security job]", got)
	}
	sc := container.SecurityContext
	if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 65532 || sc.RunAsGroup == nil || *sc.RunAsGroup != 65532 {
		t.Fatalf("securityContext = %+v", sc)
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Fatalf("securityContext = %+v", sc)
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Fatalf("readOnlyRootFilesystem = %v, want true", sc.ReadOnlyRootFilesystem)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("seccompProfile = %+v, want RuntimeDefault", sc.SeccompProfile)
	}
	if cpu := container.Resources.Limits[corev1.ResourceCPU]; cpu.MilliValue() != int64(tool.Budgets.CPU) {
		t.Fatalf("cpu limit = %s, want %dm", cpu.String(), tool.Budgets.CPU)
	}
	if memory := container.Resources.Requests[corev1.ResourceMemory]; memory.Value() != tool.Budgets.Memory {
		t.Fatalf("memory request = %s, want %d", memory.String(), tool.Budgets.Memory)
	}
	if _, ok := container.Resources.Limits[corev1.ResourceEphemeralStorage]; !ok {
		t.Fatal("ephemeral-storage limit is missing")
	}

	env := map[string]corev1.EnvVar{}
	for _, item := range container.Env {
		env[item.Name] = item
	}
	if env[securitytoolrun.EnvConfig].Value != "/ga/config/run.json" || env[securitytoolrun.EnvWorkdir].Value != "/work" {
		t.Fatalf("env = %+v", container.Env)
	}
	if env[securitytoolrun.EnvOutputPrefix].Value != "security-tool-runs/ns/scan/output" {
		t.Fatalf("output prefix = %q", env[securitytoolrun.EnvOutputPrefix].Value)
	}
	if env[securitytoolrun.EnvTargetKey].Value != "security-tool-runs/ns/scan/target.tar.gz" {
		t.Fatalf("target key = %q", env[securitytoolrun.EnvTargetKey].Value)
	}
	if env[securitytoolrun.EnvTargetDigest].Value != securityToolTestDigest {
		t.Fatalf("target digest = %q", env[securitytoolrun.EnvTargetDigest].Value)
	}
	if env["HOME"].Value != securitytoolrun.WorkDir {
		t.Fatalf("HOME = %q, want the writable work volume: the rootfs is read-only", env["HOME"].Value)
	}
	if env["S3_BUCKET"].Value != "platform-bucket" {
		t.Fatalf("S3_BUCKET = %q", env["S3_BUCKET"].Value)
	}
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		item, ok := env[name]
		if !ok || item.ValueFrom == nil || item.ValueFrom.SecretKeyRef == nil ||
			item.ValueFrom.SecretKeyRef.Name != workerInfraSecretName {
			t.Fatalf("%s = %+v, want a worker infra secret reference", name, item)
		}
	}

	for _, volume := range pod.Volumes {
		if volume.HostPath != nil {
			t.Fatalf("volume %s uses a hostPath", volume.Name)
		}
		if volume.EmptyDir != nil && volume.EmptyDir.SizeLimit == nil {
			t.Fatalf("volume %s has no size limit", volume.Name)
		}
	}
	if !ownedBySecurityToolRun(job.OwnerReferences) {
		t.Fatalf("job owner references = %+v", job.OwnerReferences)
	}

	configMap := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "scan-config"}, configMap); err != nil {
		t.Fatalf("Get(ConfigMap) error = %v", err)
	}
	if !ownedBySecurityToolRun(configMap.OwnerReferences) {
		t.Fatalf("configmap owner references = %+v", configMap.OwnerReferences)
	}
	var decoded securitytoolpacks.RunConfig
	if err := json.Unmarshal([]byte(configMap.Data["run.json"]), &decoded); err != nil {
		t.Fatalf("decoding run.json: %v", err)
	}
	if decoded.Tool != "authorization-matrix" || decoded.Target.Digest != securityToolTestDigest {
		t.Fatalf("run.json = %+v", decoded)
	}

	// Re-reconciling an already-started run must not recreate anything.
	reconcileSecurityToolRun(t, c, &stubBlobReader{})
	jobs := &batchv1.JobList{}
	if err := c.List(context.Background(), jobs, client.InNamespace("ns")); err != nil {
		t.Fatalf("List(Job) error = %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs.Items))
	}
}

func TestSecurityToolRunJobEphemeralStorageCoversScratchVolumes(t *testing.T) {
	tool := securitytoolpacks.Tool{Budgets: securitytoolpacks.Budgets{MaxOutputSize: 16 << 20}}
	job := securityToolRunJob(newSecurityToolRun(nil), tool, securityToolTestImage, metav1.OwnerReference{})
	limit := job.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceEphemeralStorage]
	var volumeTotal int64
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.EmptyDir != nil {
			volumeTotal += volume.EmptyDir.SizeLimit.Value()
		}
	}
	if limit.Cmp(*resource.NewQuantity(volumeTotal, resource.BinarySI)) < 0 {
		t.Fatalf("ephemeral-storage limit %s is below the emptyDir total %d", limit.String(), volumeTotal)
	}
}

func TestSecurityToolRunSucceedsFromManifest(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	c := newSecurityToolRunClient(t, newSecurityToolRun(nil))
	reconcileSecurityToolRun(t, c, &stubBlobReader{})
	completeSecurityToolJob(t, c, batchv1.JobComplete, "")

	manifest := securitytoolrun.Manifest{
		SchemaVersion:   securitytoolrun.ManifestSchemaVersion,
		Tool:            "authorization-matrix",
		Status:          "findings",
		FindingCount:    3,
		ResultObjectKey: securitytoolrun.ResultObjectKey("ns", "scan"),
		ResultDigest:    securityToolTestDigest,
		Artifacts: []securitytoolrun.ManifestArtifact{{
			Name:      "raw-00",
			MediaType: "application/json",
			Digest:    securityToolTestDigest,
			Size:      42,
			ObjectKey: securitytoolrun.OutputPrefix("ns", "scan") + "/raw-00",
		}},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	blobs := &stubBlobReader{objects: map[string][]byte{securitytoolrun.ManifestObjectKey("ns", "scan"): encoded}}

	run := reconcileSecurityToolRun(t, c, blobs)
	if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseSucceeded {
		t.Fatalf("phase = %q, want Succeeded", run.Status.Phase)
	}
	result := run.Status.Result
	if result == nil || result.Status != "findings" || result.FindingCount != 3 {
		t.Fatalf("result = %+v", result)
	}
	if result.ResultObjectKey != "security-tool-runs/ns/scan/output/result.json" || result.ResultDigest != securityToolTestDigest {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].ObjectKey != "security-tool-runs/ns/scan/output/raw-00" {
		t.Fatalf("artifacts = %+v", result.Artifacts)
	}
	if run.Status.CompletedAt == nil {
		t.Fatal("completedAt is not set")
	}

	// Terminal runs are never reprocessed.
	before := blobs.calls
	reconcileSecurityToolRun(t, c, blobs)
	if blobs.calls != before {
		t.Fatalf("blob reads = %d, want no additional reads after a terminal phase", blobs.calls)
	}
}

func TestSecurityToolRunFailsWhenManifestIsUnreadable(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	cases := map[string]*stubBlobReader{
		"missing":      {err: store.ErrContentBlobNotFound},
		"unparseable":  {objects: map[string][]byte{securitytoolrun.ManifestObjectKey("ns", "scan"): []byte("not json")}},
		"wrong schema": {objects: map[string][]byte{securitytoolrun.ManifestObjectKey("ns", "scan"): []byte(`{"schema_version":"v0","tool":"authorization-matrix","status":"pass"}`)}},
		"other tool": {objects: map[string][]byte{securitytoolrun.ManifestObjectKey("ns", "scan"): []byte(
			`{"schema_version":"security-tool-job-manifest/v1","tool":"nuclei","status":"pass"}`)}},
	}
	for name, blobs := range cases {
		t.Run(name, func(t *testing.T) {
			c := newSecurityToolRunClient(t, newSecurityToolRun(nil))
			reconcileSecurityToolRun(t, c, blobs)
			completeSecurityToolJob(t, c, batchv1.JobComplete, "")
			run := reconcileSecurityToolRun(t, c, blobs)
			if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseFailed {
				t.Fatalf("phase = %q, want Failed", run.Status.Phase)
			}
			if run.Status.Result == nil || run.Status.Result.Status != "error" {
				t.Fatalf("result = %+v, want status error", run.Status.Result)
			}
		})
	}
}

func TestSecurityToolRunFailedJobNeverPasses(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	passManifest := []byte(`{"schema_version":"security-tool-job-manifest/v1","tool":"authorization-matrix","status":"pass"}`)
	cases := []struct {
		name       string
		reason     string
		wantStatus string
	}{
		{name: "deadline exceeded", reason: "DeadlineExceeded", wantStatus: "timeout"},
		{name: "backoff limit", reason: "BackoffLimitExceeded", wantStatus: "error"},
		{name: "unknown reason", reason: "", wantStatus: "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blobs := &stubBlobReader{objects: map[string][]byte{securitytoolrun.ManifestObjectKey("ns", "scan"): passManifest}}
			c := newSecurityToolRunClient(t, newSecurityToolRun(nil))
			reconcileSecurityToolRun(t, c, blobs)
			completeSecurityToolJob(t, c, batchv1.JobFailed, tc.reason)

			run := reconcileSecurityToolRun(t, c, blobs)
			if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseFailed {
				t.Fatalf("phase = %q, want Failed", run.Status.Phase)
			}
			if run.Status.Result == nil || run.Status.Result.Status != tc.wantStatus {
				t.Fatalf("result = %+v, want status %s", run.Status.Result, tc.wantStatus)
			}
			if blobs.calls != 0 {
				t.Fatalf("blob reads = %d, want 0 for a failed Job", blobs.calls)
			}
		})
	}
}

func TestSecurityToolsJobTTLSeconds(t *testing.T) {
	if got := securityToolsJobTTLSeconds(); got != 600 {
		t.Fatalf("default ttl = %d, want 600", got)
	}
	t.Setenv(securityToolsJobTTLEnv, "120")
	if got := securityToolsJobTTLSeconds(); got != 120 {
		t.Fatalf("ttl = %d, want 120", got)
	}
	for _, raw := range []string{"0", "30", "-1"} {
		t.Setenv(securityToolsJobTTLEnv, raw)
		if got := securityToolsJobTTLSeconds(); got != 600 {
			t.Fatalf("ttl for %q = %d, want the default: a short TTL deletes the Job before its verdict is read", raw, got)
		}
	}
	t.Setenv(securityToolsJobTTLEnv, "not-a-number")
	if got := securityToolsJobTTLSeconds(); got != 600 {
		t.Fatalf("ttl = %d, want the default for invalid input", got)
	}
}

func TestSecurityToolRunJobNameFitsLabelLimit(t *testing.T) {
	if got := securityToolRunJobName("scan"); got != "scan-job" {
		t.Fatalf("job name = %q", got)
	}
	long := securityToolRunJobName(strings.Repeat("a", 120))
	if len(long) > 63 || !strings.HasSuffix(long, "-job") {
		t.Fatalf("job name = %q (%d chars)", long, len(long))
	}
}

func mustSecurityToolRegistry(t *testing.T) *securitytoolpacks.Registry {
	t.Helper()
	registry, err := securitytoolrun.DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry() error = %v", err)
	}
	return registry
}

func ownedBySecurityToolRun(refs []metav1.OwnerReference) bool {
	for _, ref := range refs {
		if ref.Kind == "SecurityToolRun" && ref.Name == "scan" &&
			ref.Controller != nil && *ref.Controller &&
			ref.BlockOwnerDeletion != nil && *ref.BlockOwnerDeletion {
			return true
		}
	}
	return false
}

// reconcileSecurityToolRunResult reconciles once and returns the requeue
// decision along with the observed run.
func reconcileSecurityToolRunResult(t *testing.T, c client.Client, blobs SecurityToolBlobReader) (ctrl.Result, *platformv1alpha1.SecurityToolRun) {
	t.Helper()
	r := &SecurityToolRunReconciler{Client: c, Blobs: blobs}
	key := client.ObjectKey{Namespace: "ns", Name: "scan"}
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	run := &platformv1alpha1.SecurityToolRun{}
	if err := c.Get(context.Background(), key, run); err != nil {
		t.Fatalf("Get(SecurityToolRun) error = %v", err)
	}
	return result, run
}

// A Job that vanished took its verdict with it; re-creating it would run the
// scan a second time.
func TestSecurityToolRunFailsWhenJobDisappears(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	c := newSecurityToolRunClient(t, newSecurityToolRun(nil))
	reconcileSecurityToolRun(t, c, &stubBlobReader{})
	job := getSecurityToolJob(t, c)
	if err := c.Delete(context.Background(), job); err != nil {
		t.Fatalf("Delete(Job) error = %v", err)
	}

	// Informer lag right after creation is waited out, not treated as a
	// vanished Job — and never re-executed.
	result, run := reconcileSecurityToolRunResult(t, c, &stubBlobReader{})
	if result.RequeueAfter != securityToolsRequeue || run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseRunning {
		t.Fatalf("result = %+v phase = %q", result, run.Status.Phase)
	}
	ageSecurityToolRunStart(t, c, run, securityToolsJobVisibilityGrace+time.Minute)

	run = reconcileSecurityToolRun(t, c, &stubBlobReader{})
	if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseFailed {
		t.Fatalf("phase = %q, want Failed", run.Status.Phase)
	}
	if run.Status.Result == nil || run.Status.Result.Status != "error" {
		t.Fatalf("result = %+v, want status error", run.Status.Result)
	}
	if !hasSecurityToolCondition(run, "JobDisappeared") {
		t.Fatalf("conditions = %+v, want reason JobDisappeared", run.Status.Conditions)
	}
	jobs := &batchv1.JobList{}
	if err := c.List(context.Background(), jobs, client.InNamespace("ns")); err != nil {
		t.Fatalf("List(Job) error = %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("jobs = %d, want the scan not to be re-executed", len(jobs.Items))
	}
}

func TestSecurityToolRunRejectsForeignJob(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	foreign := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "scan-job", Namespace: "ns"}}
	c := newSecurityToolRunClient(t, newSecurityToolRun(nil), foreign)

	run := reconcileSecurityToolRun(t, c, &stubBlobReader{})
	if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseFailed || !hasSecurityToolCondition(run, "JobNameConflict") {
		t.Fatalf("phase = %q conditions = %+v", run.Status.Phase, run.Status.Conditions)
	}
}

func TestSecurityToolRunRetriesTransientManifestReads(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	c := newSecurityToolRunClient(t, newSecurityToolRun(nil))
	blobs := &stubBlobReader{err: fmt.Errorf("dial tcp: connection refused")}
	reconcileSecurityToolRun(t, c, blobs)
	completeSecurityToolJob(t, c, batchv1.JobComplete, "")

	result, run := reconcileSecurityToolRunResult(t, c, blobs)
	if result.RequeueAfter != securityToolsRequeue {
		t.Fatalf("requeueAfter = %v, want %v", result.RequeueAfter, securityToolsRequeue)
	}
	if run.Status.Phase == platformv1alpha1.SecurityToolRunPhaseFailed {
		t.Fatalf("a transient object-store error must not fail a completed scan: %+v", run.Status)
	}

	// Once the Job's retention window has closed there is nothing left to wait
	// for, and the run fails.
	job := getSecurityToolJob(t, c)
	stale := metav1.NewTime(time.Now().Add(-2 * securityToolsDefaultTTLSeconds * time.Second))
	job.Status.CompletionTime = &stale
	for i := range job.Status.Conditions {
		job.Status.Conditions[i].LastTransitionTime = stale
	}
	if err := c.Status().Update(context.Background(), job); err != nil {
		t.Fatalf("Status().Update(Job) error = %v", err)
	}
	_, run = reconcileSecurityToolRunResult(t, c, blobs)
	if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseFailed || !hasSecurityToolCondition(run, "ManifestUnreadable") {
		t.Fatalf("phase = %q conditions = %+v", run.Status.Phase, run.Status.Conditions)
	}
}

func TestSecurityToolRunRejectsManifestKeysOutsideOutputPrefix(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	cases := map[string]securitytoolrun.Manifest{
		"foreign result": {
			ResultObjectKey: "security-tool-runs/ns/other/output/result.json",
			ResultDigest:    securityToolTestDigest,
		},
		"foreign artifact": {
			Artifacts: []securitytoolrun.ManifestArtifact{{
				ObjectKey: "security-tool-runs/ns/other/output/raw-00",
				Digest:    securityToolTestDigest,
			}},
		},
	}
	for name, partial := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := partial
			manifest.SchemaVersion = securitytoolrun.ManifestSchemaVersion
			manifest.Tool = "authorization-matrix"
			manifest.Status = "pass"
			encoded, err := json.Marshal(manifest)
			if err != nil {
				t.Fatalf("marshal manifest: %v", err)
			}
			blobs := &stubBlobReader{objects: map[string][]byte{securitytoolrun.ManifestObjectKey("ns", "scan"): encoded}}
			c := newSecurityToolRunClient(t, newSecurityToolRun(nil))
			reconcileSecurityToolRun(t, c, blobs)
			completeSecurityToolJob(t, c, batchv1.JobComplete, "")
			run := reconcileSecurityToolRun(t, c, blobs)
			if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseFailed || !hasSecurityToolCondition(run, "ManifestInvalid") {
				t.Fatalf("phase = %q conditions = %+v", run.Status.Phase, run.Status.Conditions)
			}
		})
	}
}

// The CRD caps the status payload; an oversized manifest must be clamped
// rather than rejected by the API server on every patch.
func TestSecurityToolRunClampsOversizedManifestStatus(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	manifest := securitytoolrun.Manifest{
		SchemaVersion:   securitytoolrun.ManifestSchemaVersion,
		Tool:            "authorization-matrix",
		Status:          "findings",
		ResultObjectKey: securitytoolrun.ResultObjectKey("ns", "scan"),
		ResultDigest:    securityToolTestDigest,
	}
	for i := 0; i < 100; i++ {
		manifest.Errors = append(manifest.Errors, strings.Repeat("x", 4096))
		manifest.Artifacts = append(manifest.Artifacts, securitytoolrun.ManifestArtifact{
			ObjectKey: fmt.Sprintf("%s/raw-%03d", securitytoolrun.OutputPrefix("ns", "scan"), i),
			Digest:    securityToolTestDigest,
		})
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	blobs := &stubBlobReader{objects: map[string][]byte{securitytoolrun.ManifestObjectKey("ns", "scan"): encoded}}
	c := newSecurityToolRunClient(t, newSecurityToolRun(nil))
	reconcileSecurityToolRun(t, c, blobs)
	completeSecurityToolJob(t, c, batchv1.JobComplete, "")

	run := reconcileSecurityToolRun(t, c, blobs)
	if run.Status.Phase != platformv1alpha1.SecurityToolRunPhaseSucceeded {
		t.Fatalf("phase = %q, want Succeeded", run.Status.Phase)
	}
	result := run.Status.Result
	if len(result.Errors) != securityToolMaxStatusErrors || len(result.Artifacts) != securityToolMaxStatusArtifacts {
		t.Fatalf("errors = %d artifacts = %d, want %d and %d",
			len(result.Errors), len(result.Artifacts), securityToolMaxStatusErrors, securityToolMaxStatusArtifacts)
	}
	for _, message := range result.Errors {
		if len(message) > securityToolMaxStatusErrorBytes {
			t.Fatalf("error length = %d, want at most %d", len(message), securityToolMaxStatusErrorBytes)
		}
	}
}

// Customer source archives are not retained past the run; results are.
func TestSecurityToolRunDeletesStagedTargetWhenTerminal(t *testing.T) {
	t.Setenv(securityToolsImageEnv, securityToolTestImage)
	manifest := securitytoolrun.Manifest{
		SchemaVersion:   securitytoolrun.ManifestSchemaVersion,
		Tool:            "authorization-matrix",
		Status:          "pass",
		ResultObjectKey: securitytoolrun.ResultObjectKey("ns", "scan"),
		ResultDigest:    securityToolTestDigest,
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	targetKey := securitytoolrun.TargetObjectKey("ns", "scan")
	blobs := &stubBlobReader{objects: map[string][]byte{
		securitytoolrun.ManifestObjectKey("ns", "scan"): encoded,
		targetKey: []byte("archive"),
	}}
	c := newSecurityToolRunClient(t, newSecurityToolRun(nil))
	reconcileSecurityToolRun(t, c, blobs)
	if len(blobs.deleted) != 0 {
		t.Fatalf("deleted = %v, want nothing while the run is active", blobs.deleted)
	}
	completeSecurityToolJob(t, c, batchv1.JobComplete, "")
	reconcileSecurityToolRun(t, c, blobs)
	if len(blobs.deleted) != 1 || blobs.deleted[0] != targetKey {
		t.Fatalf("deleted = %v, want only %s", blobs.deleted, targetKey)
	}

	// A failed run drops the archive too.
	failing := &stubBlobReader{objects: map[string][]byte{targetKey: []byte("archive")}, err: store.ErrContentBlobNotFound}
	c = newSecurityToolRunClient(t, newSecurityToolRun(nil))
	reconcileSecurityToolRun(t, c, failing)
	completeSecurityToolJob(t, c, batchv1.JobComplete, "")
	reconcileSecurityToolRun(t, c, failing)
	if len(failing.deleted) != 1 || failing.deleted[0] != targetKey {
		t.Fatalf("deleted = %v, want only %s", failing.deleted, targetKey)
	}

	// A run asserting another run's staged key never deletes it.
	foreignKey := securitytoolrun.TargetObjectKey("ns", "other")
	foreign := &stubBlobReader{objects: map[string][]byte{foreignKey: []byte("archive")}}
	c = newSecurityToolRunClient(t, newSecurityToolRun(func(run *platformv1alpha1.SecurityToolRun) {
		run.Spec.Target.StagedObjectKey = foreignKey
	}))
	reconcileSecurityToolRun(t, c, foreign)
	if len(foreign.deleted) != 0 {
		t.Fatalf("deleted = %v, want nothing outside this run's prefix", foreign.deleted)
	}
}

func TestSecurityToolRunChildNamesAreBoundedAndDistinct(t *testing.T) {
	if got := securityToolRunConfigMapName("scan"); got != "scan-config" {
		t.Fatalf("configmap name = %q", got)
	}
	first := strings.Repeat("a", 300)
	second := first[:299] + "b"
	for _, derive := range []struct {
		name  string
		fn    func(string) string
		limit int
	}{
		{name: "job", fn: securityToolRunJobName, limit: 63},
		{name: "configmap", fn: securityToolRunConfigMapName, limit: 253},
	} {
		t.Run(derive.name, func(t *testing.T) {
			a, b := derive.fn(first), derive.fn(second)
			if len(a) > derive.limit || len(b) > derive.limit {
				t.Fatalf("names %d/%d exceed the %d-character limit", len(a), len(b), derive.limit)
			}
			if a == b {
				t.Fatalf("two long SecurityToolRun names collided on %q", a)
			}
		})
	}
}

func ageSecurityToolRunStart(t *testing.T, c client.Client, run *platformv1alpha1.SecurityToolRun, age time.Duration) {
	t.Helper()
	started := metav1.NewTime(time.Now().Add(-age))
	run.Status.StartedAt = &started
	if err := c.Status().Update(context.Background(), run); err != nil {
		t.Fatalf("Status().Update(SecurityToolRun) error = %v", err)
	}
}

func hasSecurityToolCondition(run *platformv1alpha1.SecurityToolRun, reason string) bool {
	for _, condition := range run.Status.Conditions {
		if condition.Reason == reason {
			return true
		}
	}
	return false
}
