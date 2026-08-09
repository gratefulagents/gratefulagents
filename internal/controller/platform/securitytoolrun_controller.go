/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolrun"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/contentblob"
)

const (
	securityToolsImageEnv          = "SECURITY_TOOLS_IMAGE"
	securityToolsAllowUnpinnedEnv  = "SECURITY_TOOLS_ALLOW_UNPINNED_IMAGE"
	securityToolsJobTTLEnv         = "SECURITY_TOOLS_JOB_TTL_SECONDS"
	securityToolsDefaultTTLSeconds = 600
	// securityToolsMinTTLSeconds keeps a finished Job around long enough for
	// the controller to observe its terminal condition and read the manifest.
	securityToolsMinTTLSeconds = 60
	// securityToolsDeadlineSlack covers staging, image pull, and result upload
	// on top of the registry timeout budget for the scan itself.
	securityToolsDeadlineSlack = 120 * time.Second
	securityToolsRequeue       = 15 * time.Second
	// securityToolsJobVisibilityGrace absorbs informer lag: a Job created
	// moments ago may not be in the cache yet, and that is not a vanished Job.
	securityToolsJobVisibilityGrace = time.Minute
	// securityToolsRunAsID is the nonroot uid/gid baked into the security
	// tools image.
	securityToolsRunAsID int64 = 65532
	// The status payload the Job supplies is clamped to the CRD limits so an
	// oversized manifest cannot make every status patch fail.
	securityToolMaxStatusErrors     = 32
	securityToolMaxStatusErrorBytes = 1 << 10
	securityToolMaxStatusArtifacts  = 64

	// securityToolStagedCleanupCondition durably records whether the staged
	// customer archive has been dropped, so a transient object-store failure
	// is retried by later reconciles instead of retaining the archive forever.
	securityToolStagedCleanupCondition = "StagedTargetCleanup"
	// securityToolStagedCleanupAbandonedReason marks the retry window closed:
	// the archive may still exist and the run says so.
	securityToolStagedCleanupAbandonedReason = "Abandoned"
	securityToolsStagedCleanupBackoff        = 15 * time.Second
	securityToolsStagedCleanupMaxBackoff     = 5 * time.Minute
	securityToolsStagedCleanupDeadline       = 30 * time.Minute
)

// SecurityToolBlobReader reads Job output from object storage. Tests inject a
// stub; production uses the platform S3 bucket.
type SecurityToolBlobReader interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

// SecurityToolBlobDeleter is implemented by stores that can drop the staged
// target archive once a run is terminal.
type SecurityToolBlobDeleter interface {
	Delete(ctx context.Context, key string) error
}

// SecurityToolRunReconciler executes one registered, deterministic security
// tool per SecurityToolRun in a short-lived Kubernetes Job. Neither the tool
// argv nor the image comes from the request: both are derived from the pinned
// registry and operator configuration.
type SecurityToolRunReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Registry validates requests. Nil builds the compiled default registry.
	Registry *securitytoolpacks.Registry
	// Blobs reads Job output. Nil builds the S3 store from operator env.
	Blobs SecurityToolBlobReader
}

func (r *SecurityToolRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	run := &platformv1alpha1.SecurityToolRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if isTerminalSecurityToolRun(run) {
		return r.reconcileStagedCleanup(ctx, run)
	}

	registry := r.Registry
	if registry == nil {
		built, err := securitytoolrun.DefaultRegistry()
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("building security tool registry: %w", err)
		}
		registry = built
	}

	request, err := securitytoolrun.RunConfigFor(run.Spec)
	if err != nil {
		return ctrl.Result{}, r.failRun(ctx, run, "InvalidRequest", err.Error(), "error")
	}
	tool, err := securitytoolrun.Validate(registry, request)
	if err != nil {
		return ctrl.Result{}, r.failRun(ctx, run, "InvalidRequest", err.Error(), "error")
	}
	if staged := strings.TrimSpace(run.Spec.Target.StagedObjectKey); staged != "" {
		if expected := securitytoolrun.TargetObjectKey(run.Namespace, run.Name); staged != expected {
			return ctrl.Result{}, r.failRun(ctx, run, "InvalidRequest",
				fmt.Sprintf("staged target must be %s, got %s", expected, staged), "error")
		}
	}
	if securitytoolrun.NeedsNetworkAuthorization(tool, request) {
		authorized, err := r.authorizedNetworkTargets(ctx, run)
		if err != nil {
			var refusal *securityToolRunRefusal
			if errors.As(err, &refusal) {
				return ctrl.Result{}, r.failRun(ctx, run, refusal.reason, refusal.message, "error")
			}
			return ctrl.Result{}, err
		}
		if err := securitytoolrun.AuthorizeNetworkTargets(authorized, request); err != nil {
			return ctrl.Result{}, r.failRun(ctx, run, "NetworkTargetNotAuthorized", err.Error(), "error")
		}
	}
	image, err := resolveSecurityToolsImage()
	if err != nil {
		return ctrl.Result{}, r.failRun(ctx, run, "InvalidImage", err.Error(), "error")
	}

	job := &batchv1.Job{}
	jobKey := client.ObjectKey{Namespace: run.Namespace, Name: securityToolRunJobName(run.Name)}
	switch err := r.Get(ctx, jobKey, job); {
	case apierrors.IsNotFound(err):
		// A Job this run already started and that no longer exists took its
		// verdict with it. Re-creating it would re-execute a scan the
		// requester already believes is running. A freshly created Job may
		// simply not be in the cache yet, so wait it out first.
		if run.Status.JobName != "" || run.Status.Phase == platformv1alpha1.SecurityToolRunPhaseRunning {
			if started := run.Status.StartedAt; started != nil && time.Since(started.Time) < securityToolsJobVisibilityGrace {
				return ctrl.Result{RequeueAfter: securityToolsRequeue}, nil
			}
			return ctrl.Result{}, r.failRun(ctx, run, "JobDisappeared",
				fmt.Sprintf("execution Job %s no longer exists; a scan is never re-executed", jobKey.Name), "error")
		}
		if err := r.startJob(ctx, run, request.RunConfig, tool, image); err != nil {
			var refusal *securityToolRunRefusal
			if errors.As(err, &refusal) {
				return ctrl.Result{}, r.failRun(ctx, run, refusal.reason, refusal.message, "error")
			}
			return ctrl.Result{}, err
		}
		log.Info("SecurityToolRun job created", "name", run.Name, "job", jobKey.Name, "tool", tool.Name)
		return ctrl.Result{RequeueAfter: securityToolsRequeue}, r.markRunning(ctx, run, jobKey.Name, image)
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("getting Job %s: %w", jobKey.Name, err)
	}
	if !metav1.IsControlledBy(job, run) {
		return ctrl.Result{}, r.failRun(ctx, run, "JobNameConflict",
			fmt.Sprintf("Job %s is not controlled by this SecurityToolRun", jobKey.Name), "error")
	}

	complete, failed, failureReason := securityToolJobState(job)
	switch {
	case complete:
		return r.completeRun(ctx, run, job)
	case failed:
		resultStatus := "error"
		if failureReason == "DeadlineExceeded" {
			resultStatus = "timeout"
		}
		message := fmt.Sprintf("execution Job %s failed", job.Name)
		if failureReason != "" {
			message = fmt.Sprintf("execution Job %s failed: %s", job.Name, failureReason)
		}
		return ctrl.Result{}, r.failRun(ctx, run, "JobFailed", message, resultStatus)
	default:
		return ctrl.Result{RequeueAfter: securityToolsRequeue}, r.markRunning(ctx, run, job.Name, image)
	}
}

// securityToolRunRefusal is a reason to refuse a request outright rather than
// retry it: the run is failed with this reason and message, and nothing is
// created.
type securityToolRunRefusal struct {
	reason  string
	message string
}

func (e *securityToolRunRefusal) Error() string { return e.message }

// authorizedNetworkTargets reads the network authorization stamped by the
// platform onto the AgentRun that owns this request. The list is operator
// configuration reached through ownership, never anything the requesting model
// supplied: a run whose owner cannot be read, or that carries no
// authorization, may not touch the network at all.
func (r *SecurityToolRunReconciler) authorizedNetworkTargets(ctx context.Context, run *platformv1alpha1.SecurityToolRun) ([]string, error) {
	for _, ref := range run.OwnerReferences {
		if ref.Kind != "AgentRun" {
			continue
		}
		group, _, _ := strings.Cut(ref.APIVersion, "/")
		if group != platformv1alpha1.GroupVersion.Group {
			continue
		}
		owner := &platformv1alpha1.AgentRun{}
		key := client.ObjectKey{Namespace: run.Namespace, Name: ref.Name}
		if err := r.Get(ctx, key, owner); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, &securityToolRunRefusal{
					reason: "NetworkAuthorizationUnavailable",
					message: fmt.Sprintf("owning AgentRun %s does not exist; "+
						"network security tools run only under an authorized scan run", ref.Name),
				}
			}
			return nil, fmt.Errorf("reading owning AgentRun %s: %w", ref.Name, err)
		}
		targets := securitytoolrun.SplitAuthorizedNetworkTargets(
			owner.Annotations[triggersv1alpha1.SecurityScanAuthorizedNetworkTargetsAnnotation])
		if len(targets) == 0 {
			return nil, &securityToolRunRefusal{
				reason: "NetworkAuthorizationUnavailable",
				message: fmt.Sprintf("owning AgentRun %s carries no %s authorization; "+
					"no network scanning is authorized for this run", ref.Name,
					triggersv1alpha1.SecurityScanAuthorizedNetworkTargetsAnnotation),
			}
		}
		return targets, nil
	}
	return nil, &securityToolRunRefusal{
		reason: "NetworkAuthorizationUnavailable",
		message: "this request has no owning AgentRun to read network authorization from; " +
			"network security tools run only under an authorized scan run",
	}
}

func isTerminalSecurityToolRun(run *platformv1alpha1.SecurityToolRun) bool {
	return run.Status.Phase == platformv1alpha1.SecurityToolRunPhaseSucceeded ||
		run.Status.Phase == platformv1alpha1.SecurityToolRunPhaseFailed
}

// securityToolJobState reports the terminal Job conditions. backoffLimit is 0,
// so a Job never retries and these conditions are final.
func securityToolJobState(job *batchv1.Job) (complete, failed bool, failureReason string) {
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobComplete:
			complete = true
		case batchv1.JobFailed:
			failed = true
			failureReason = condition.Reason
		}
	}
	// A failed Job never becomes a pass, whatever else it reported.
	if failed {
		return false, true, failureReason
	}
	return complete, false, ""
}

// resolveSecurityToolsImage requires a digest-pinned image so an execution can
// be replayed against exactly the bytes that produced it.
func resolveSecurityToolsImage() (string, error) {
	image := strings.TrimSpace(os.Getenv(securityToolsImageEnv))
	if image == "" {
		return "", fmt.Errorf("%s is not configured on the controller manager", securityToolsImageEnv)
	}
	if strings.Contains(image, "@sha256:") {
		return image, nil
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv(securityToolsAllowUnpinnedEnv)), "true") {
		return image, nil
	}
	return "", fmt.Errorf("%s %q must be digest pinned (image@sha256:...); set %s=true to override",
		securityToolsImageEnv, image, securityToolsAllowUnpinnedEnv)
}

func securityToolsJobTTLSeconds() int32 {
	raw := strings.TrimSpace(os.Getenv(securityToolsJobTTLEnv))
	if raw == "" {
		return securityToolsDefaultTTLSeconds
	}
	seconds, err := strconv.Atoi(raw)
	// A short TTL deletes the Job before its Complete condition can be
	// observed, which reads as a vanished Job and loses the verdict.
	if err != nil || seconds < securityToolsMinTTLSeconds || seconds > math.MaxInt32 {
		return securityToolsDefaultTTLSeconds
	}
	return int32(seconds)
}

// securityToolRunJobName keeps the Job name within the 63-character limit that
// generated pod names depend on. SecurityToolRun names may be a full DNS
// subdomain (253 characters).
func securityToolRunJobName(name string) string {
	return securityToolRunChildName(name, "-job", 63)
}

func securityToolRunConfigMapName(name string) string {
	return securityToolRunChildName(name, "-config", 253)
}

// securityToolRunChildName derives a child-object name that fits its DNS
// limit. A truncated name carries a hash of the full name so two long
// SecurityToolRun names cannot collide on one Job or ConfigMap.
func securityToolRunChildName(name, suffix string, limit int) string {
	if len(name)+len(suffix) <= limit {
		return name + suffix
	}
	sum := sha256.Sum256([]byte(name))
	hash := hex.EncodeToString(sum[:])[:8]
	keep := limit - len(suffix) - len(hash) - 1
	return strings.Trim(name[:keep], "-.") + "-" + hash + suffix
}

func truncateSecurityToolRunName(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	return strings.Trim(name[:limit], "-.")
}

// startJob materializes the immutable execution inputs: a ConfigMap holding
// the typed RunConfig and the Job that consumes it. Both are owned by the
// SecurityToolRun so deletion garbage-collects them.
func (r *SecurityToolRunReconciler) startJob(ctx context.Context, run *platformv1alpha1.SecurityToolRun, config securitytoolpacks.RunConfig, tool securitytoolpacks.Tool, image string) error {
	if err := ensureWorkerInfraSecret(ctx, r.Client, run.Namespace); err != nil {
		return fmt.Errorf("syncing worker infra secret: %w", err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encoding run config: %w", err)
	}
	owner := securityToolRunOwnerRef(run)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            securityToolRunConfigMapName(run.Name),
			Namespace:       run.Namespace,
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		// Agent service accounts may create ConfigMaps in their namespace, so
		// the execution inputs are pinned: an immutable ConfigMap cannot be
		// edited after the Job mounts it, and a pre-existing one is only
		// accepted when this run owns it and its bytes are identical.
		Immutable: new(true),
		Data:      map[string]string{securitytoolrun.ConfigFileName: string(encoded)},
	}
	if err := r.Create(ctx, configMap); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating ConfigMap %s: %w", configMap.Name, err)
		}
		if err := r.verifyExecutionConfigMap(ctx, run, configMap); err != nil {
			return err
		}
	}
	job := securityToolRunJob(run, tool, image, owner)
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating Job %s: %w", job.Name, err)
	}
	return nil
}

// verifyExecutionConfigMap accepts an already-existing execution ConfigMap
// only when this run created it and its bytes are exactly the ones this
// reconcile would write. Anything else — a foreign object, edited data, or a
// mutable ConfigMap whose content could still change under the running Job —
// refuses the execution instead of mounting attacker-chosen configuration.
func (r *SecurityToolRunReconciler) verifyExecutionConfigMap(ctx context.Context, run *platformv1alpha1.SecurityToolRun, want *corev1.ConfigMap) error {
	existing := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(want), existing); err != nil {
		return fmt.Errorf("reading ConfigMap %s: %w", want.Name, err)
	}
	conflict := func(detail string) error {
		return &securityToolRunRefusal{
			reason:  "ConfigMapConflict",
			message: fmt.Sprintf("execution ConfigMap %s %s; refusing to run against it", want.Name, detail),
		}
	}
	switch {
	case !metav1.IsControlledBy(existing, run):
		return conflict("already exists and is not controlled by this SecurityToolRun")
	case existing.Immutable == nil || !*existing.Immutable:
		return conflict("already exists and is mutable")
	case !maps.Equal(existing.Data, want.Data) || len(existing.BinaryData) != 0:
		return conflict("already exists with different content")
	}
	return nil
}

func securityToolRunOwnerRef(run *platformv1alpha1.SecurityToolRun) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         platformv1alpha1.GroupVersion.String(),
		Kind:               "SecurityToolRun",
		Name:               run.Name,
		UID:                run.UID,
		Controller:         new(true),
		BlockOwnerDeletion: new(true),
	}
}

// securityToolRunJob builds the hardened execution Job. The container argv is
// fixed: no part of the request reaches the command line, the tool reads its
// typed configuration from the mounted ConfigMap instead.
func securityToolRunJob(run *platformv1alpha1.SecurityToolRun, tool securitytoolpacks.Tool, image string, owner metav1.OwnerReference) *batchv1.Job {
	workLimit := max(tool.Budgets.MaxOutputSize*4, 1<<30)
	const tmpLimit int64 = 256 << 20
	env := []corev1.EnvVar{
		{Name: securitytoolrun.EnvConfig, Value: securitytoolrun.ConfigPath},
		{Name: securitytoolrun.EnvWorkdir, Value: securitytoolrun.WorkDir},
		{Name: securitytoolrun.EnvTargetKey, Value: strings.TrimSpace(run.Spec.Target.StagedObjectKey)},
		{Name: securitytoolrun.EnvTargetDigest, Value: strings.TrimSpace(run.Spec.Target.Digest)},
		{Name: securitytoolrun.EnvOutputPrefix, Value: securitytoolrun.OutputPrefix(run.Namespace, run.Name)},
		// The container rootfs is read-only: everything the run writes,
		// including tool caches under HOME, belongs in the work emptyDir.
		{Name: "HOME", Value: securitytoolrun.WorkDir},
	}
	for _, name := range []string{"S3_BUCKET", "S3_ENDPOINT", "S3_REGION"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			env = append(env, corev1.EnvVar{Name: name, Value: value})
		}
	}
	env = append(env,
		workerInfraSecretEnv("AWS_ACCESS_KEY_ID", workerInfraKeyAWSAccessKeyID),
		workerInfraSecretEnv("AWS_SECRET_ACCESS_KEY", workerInfraKeyAWSSecretAccessKey),
	)

	backoffLimit := int32(0)
	ttl := securityToolsJobTTLSeconds()
	deadline := int64((tool.Budgets.Timeout + securityToolsDeadlineSlack).Seconds())
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            securityToolRunJobName(run.Name),
			Namespace:       run.Namespace,
			OwnerReferences: []metav1.OwnerReference{owner},
			Labels: map[string]string{
				"app.kubernetes.io/name":                      "gratefulagents-security-tool",
				"platform.gratefulagents.dev/securitytoolrun": truncateSecurityToolRunName(run.Name, 63),
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   &deadline,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: new(false),
					Containers: []corev1.Container{{
						Name:       "security-tool",
						Image:      image,
						Command:    []string{"ga-security", "job"},
						WorkingDir: securitytoolrun.WorkDir,
						Env:        env,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    *resource.NewMilliQuantity(int64(tool.Budgets.CPU), resource.DecimalSI),
								corev1.ResourceMemory: *resource.NewQuantity(tool.Budgets.Memory, resource.BinarySI),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:              *resource.NewMilliQuantity(int64(tool.Budgets.CPU), resource.DecimalSI),
								corev1.ResourceMemory:           *resource.NewQuantity(tool.Budgets.Memory, resource.BinarySI),
								corev1.ResourceEphemeralStorage: *resource.NewQuantity(workLimit+2*tmpLimit, resource.BinarySI),
							},
						},
						SecurityContext: &corev1.SecurityContext{
							RunAsUser:                new(securityToolsRunAsID),
							RunAsGroup:               new(securityToolsRunAsID),
							RunAsNonRoot:             new(true),
							AllowPrivilegeEscalation: new(false),
							ReadOnlyRootFilesystem:   new(true),
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "work", MountPath: securitytoolrun.WorkDir},
							{Name: "tmp", MountPath: "/tmp"},
							{Name: "config", MountPath: securitytoolrun.ConfigMountPath, ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
							SizeLimit: resource.NewQuantity(workLimit, resource.BinarySI),
						}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
							SizeLimit: resource.NewQuantity(tmpLimit, resource.BinarySI),
						}}},
						{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: securityToolRunConfigMapName(run.Name)},
						}}},
					},
				},
			},
		},
	}
}

// completeRun reads the Job manifest and records the typed verdict. A missing
// or untrustworthy manifest fails the run; an object store that is merely
// unreachable is retried until the Job's own retention window closes, so a
// finished scan is not thrown away over a transient error.
func (r *SecurityToolRunReconciler) completeRun(ctx context.Context, run *platformv1alpha1.SecurityToolRun, job *batchv1.Job) (ctrl.Result, error) {
	blobs, err := r.blobStore()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building object store client: %w", err)
	}
	key := securitytoolrun.ManifestObjectKey(run.Namespace, run.Name)
	raw, err := blobs.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, store.ErrContentBlobNotFound) && !manifestReadWindowClosed(job, time.Now()) {
			logf.FromContext(ctx).Info("SecurityToolRun manifest is temporarily unreadable; retrying",
				"name", run.Name, "key", key, "error", err.Error())
			return ctrl.Result{RequeueAfter: securityToolsRequeue}, nil
		}
		return ctrl.Result{}, r.failRun(ctx, run, "ManifestUnreadable", fmt.Sprintf("reading %s: %v", key, err), "error")
	}
	var manifest securitytoolrun.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ctrl.Result{}, r.failRun(ctx, run, "ManifestInvalid", fmt.Sprintf("parsing %s: %v", key, err), "error")
	}
	if err := manifest.ValidateFor(securitytoolrun.OutputPrefix(run.Namespace, run.Name)); err != nil {
		return ctrl.Result{}, r.failRun(ctx, run, "ManifestInvalid", fmt.Sprintf("validating %s: %v", key, err), "error")
	}
	if manifest.Tool != run.Spec.Tool {
		return ctrl.Result{}, r.failRun(ctx, run, "ManifestInvalid",
			fmt.Sprintf("manifest reports tool %q, expected %q", manifest.Tool, run.Spec.Tool), "error")
	}

	result := &platformv1alpha1.SecurityToolRunResult{
		Status:          manifest.Status,
		FindingCount:    clampInt32(manifest.FindingCount),
		ResultObjectKey: manifest.ResultObjectKey,
		ResultDigest:    manifest.ResultDigest,
		Errors:          clampSecurityToolErrors(manifest.Errors),
	}
	for _, artifact := range manifest.Artifacts {
		if len(result.Artifacts) == securityToolMaxStatusArtifacts {
			break
		}
		result.Artifacts = append(result.Artifacts, platformv1alpha1.SecurityToolRunArtifact{
			Name:      artifact.Name,
			MediaType: artifact.MediaType,
			Digest:    artifact.Digest,
			Size:      artifact.Size,
			ObjectKey: artifact.ObjectKey,
		})
	}
	message := fmt.Sprintf("tool %s reported %s with %d findings", manifest.Tool, manifest.Status, manifest.FindingCount)
	if err := r.patchStatus(ctx, run, func(fresh *platformv1alpha1.SecurityToolRun) {
		fresh.Status.Phase = platformv1alpha1.SecurityToolRunPhaseSucceeded
		fresh.Status.Message = message
		fresh.Status.Result = result
		fresh.Status.CompletedAt = new(metav1.Now())
		setCondition(&fresh.Status.Conditions, "Ready", metav1.ConditionFalse, "Completed", message)
		setCondition(&fresh.Status.Conditions, "Completed", metav1.ConditionTrue, "JobComplete", message)
	}); err != nil {
		return ctrl.Result{}, err
	}
	settled, err := r.deleteStagedTarget(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !settled {
		return ctrl.Result{RequeueAfter: securityToolsStagedCleanupBackoff}, nil
	}
	return ctrl.Result{}, nil
}

// manifestReadWindowClosed bounds manifest read retries by the Job's own
// retention: once Kubernetes may have deleted the Job there is nothing left to
// reconcile against.
func manifestReadWindowClosed(job *batchv1.Job, now time.Time) bool {
	completed := metav1.Time{}
	if job.Status.CompletionTime != nil {
		completed = *job.Status.CompletionTime
	} else {
		for _, condition := range job.Status.Conditions {
			if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
				completed = condition.LastTransitionTime
			}
		}
	}
	if completed.IsZero() {
		return false
	}
	return now.After(completed.Add(time.Duration(securityToolsJobTTLSeconds()) * time.Second))
}

func (r *SecurityToolRunReconciler) blobStore() (SecurityToolBlobReader, error) {
	if r.Blobs != nil {
		return r.Blobs, nil
	}
	return contentblob.NewS3FromEnv()
}

// deleteStagedTarget drops the customer source archive once the run is
// terminal. Results and raw artifacts stay: the agent reads them afterwards.
// Only this run's own key is ever deleted, whatever the spec asserted.
//
// The outcome is recorded durably in the StagedTargetCleanup condition so a
// transient object-store failure is retried by later reconciles — a terminal
// run whose archive is still there is not done — instead of being lost to a
// log line. It reports whether the cleanup has settled, either because the
// archive is gone or because the retry window closed.
func (r *SecurityToolRunReconciler) deleteStagedTarget(ctx context.Context, run *platformv1alpha1.SecurityToolRun) (bool, error) {
	key := securitytoolrun.TargetObjectKey(run.Namespace, run.Name)
	if strings.TrimSpace(run.Spec.Target.StagedObjectKey) != key {
		return true, r.recordStagedCleanup(ctx, run, metav1.ConditionTrue, "NotStaged",
			"this run staged no target archive of its own")
	}
	failure := ""
	blobs, err := r.blobStore()
	switch {
	case err != nil:
		failure = err.Error()
	default:
		deleter, ok := blobs.(SecurityToolBlobDeleter)
		if !ok {
			return true, r.recordStagedCleanup(ctx, run, metav1.ConditionTrue, "DeleteUnsupported",
				"the configured object store cannot delete objects")
		}
		if err := deleter.Delete(ctx, key); err != nil {
			failure = err.Error()
		}
	}
	if failure == "" {
		return true, r.recordStagedCleanup(ctx, run, metav1.ConditionTrue, "Deleted",
			fmt.Sprintf("staged target %s was deleted", key))
	}
	logf.FromContext(ctx).Info("staged target was not deleted", "name", run.Name, "key", key, "error", failure)
	message := fmt.Sprintf("staged target %s was not deleted: %s", key, failure)
	if stagedCleanupElapsed(run, time.Now()) >= securityToolsStagedCleanupDeadline {
		return true, r.recordStagedCleanup(ctx, run, metav1.ConditionFalse, securityToolStagedCleanupAbandonedReason,
			fmt.Sprintf("%s; giving up after %s", message, securityToolsStagedCleanupDeadline))
	}
	return false, r.recordStagedCleanup(ctx, run, metav1.ConditionFalse, "DeletePending", message)
}

// reconcileStagedCleanup keeps retrying the archive deletion of a terminal run
// until it succeeds or the retry window closes.
func (r *SecurityToolRunReconciler) reconcileStagedCleanup(ctx context.Context, run *platformv1alpha1.SecurityToolRun) (ctrl.Result, error) {
	if stagedCleanupSettled(run) {
		return ctrl.Result{}, nil
	}
	settled, err := r.deleteStagedTarget(ctx, run)
	if err != nil || settled {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: stagedCleanupBackoff(stagedCleanupElapsed(run, time.Now()))}, nil
}

// stagedCleanupSettled reports whether the archive still needs attention.
func stagedCleanupSettled(run *platformv1alpha1.SecurityToolRun) bool {
	condition := apimeta.FindStatusCondition(run.Status.Conditions, securityToolStagedCleanupCondition)
	if condition == nil {
		return false
	}
	return condition.Status == metav1.ConditionTrue || condition.Reason == securityToolStagedCleanupAbandonedReason
}

func stagedCleanupElapsed(run *platformv1alpha1.SecurityToolRun, now time.Time) time.Duration {
	if run.Status.CompletedAt == nil {
		return 0
	}
	return now.Sub(run.Status.CompletedAt.Time)
}

// stagedCleanupBackoff spaces retries out roughly geometrically: the next
// attempt is one elapsed-time away, bounded on both ends.
func stagedCleanupBackoff(elapsed time.Duration) time.Duration {
	return min(max(elapsed, securityToolsStagedCleanupBackoff), securityToolsStagedCleanupMaxBackoff)
}

func (r *SecurityToolRunReconciler) recordStagedCleanup(ctx context.Context, run *platformv1alpha1.SecurityToolRun,
	status metav1.ConditionStatus, reason, message string) error {
	if len(message) > securityToolMaxStatusErrorBytes {
		message = message[:securityToolMaxStatusErrorBytes]
	}
	existing := apimeta.FindStatusCondition(run.Status.Conditions, securityToolStagedCleanupCondition)
	if existing != nil && existing.Status == status && existing.Reason == reason && existing.Message == message {
		return nil
	}
	return r.patchStatus(ctx, run, func(fresh *platformv1alpha1.SecurityToolRun) {
		setCondition(&fresh.Status.Conditions, securityToolStagedCleanupCondition, status, reason, message)
		// A retained archive is an operator-visible outcome, not just a
		// condition: it belongs in the run's own message.
		if reason == securityToolStagedCleanupAbandonedReason {
			fresh.Status.Message = appendSecurityToolRunMessage(fresh.Status.Message, message)
		}
	})
}

func appendSecurityToolRunMessage(existing, addition string) string {
	if strings.TrimSpace(existing) == "" {
		return addition
	}
	if strings.Contains(existing, addition) {
		return existing
	}
	joined := existing + "; " + addition
	if len(joined) > securityToolMaxStatusErrorBytes {
		joined = joined[:securityToolMaxStatusErrorBytes]
	}
	return joined
}

// clampSecurityToolErrors keeps a Job-supplied error list inside the CRD
// limits so an oversized manifest cannot wedge the status patch.
func clampSecurityToolErrors(errs []string) []string {
	if len(errs) > securityToolMaxStatusErrors {
		errs = errs[:securityToolMaxStatusErrors]
	}
	clamped := make([]string, 0, len(errs))
	for _, message := range errs {
		if len(message) > securityToolMaxStatusErrorBytes {
			message = message[:securityToolMaxStatusErrorBytes]
		}
		clamped = append(clamped, message)
	}
	if len(clamped) == 0 {
		return nil
	}
	return clamped
}

func (r *SecurityToolRunReconciler) markRunning(ctx context.Context, run *platformv1alpha1.SecurityToolRun, jobName, image string) error {
	return r.patchStatus(ctx, run, func(fresh *platformv1alpha1.SecurityToolRun) {
		fresh.Status.Phase = platformv1alpha1.SecurityToolRunPhaseRunning
		fresh.Status.JobName = jobName
		fresh.Status.Image = image
		if fresh.Status.StartedAt == nil {
			fresh.Status.StartedAt = new(metav1.Now())
		}
		message := fmt.Sprintf("execution Job %s is running", jobName)
		fresh.Status.Message = message
		setCondition(&fresh.Status.Conditions, "Ready", metav1.ConditionTrue, "JobRunning", message)
		setCondition(&fresh.Status.Conditions, "Completed", metav1.ConditionFalse, "JobRunning", message)
	})
}

func (r *SecurityToolRunReconciler) failRun(ctx context.Context, run *platformv1alpha1.SecurityToolRun, reason, message, resultStatus string) error {
	if len(message) > securityToolMaxStatusErrorBytes {
		message = message[:securityToolMaxStatusErrorBytes]
	}
	if err := r.patchStatus(ctx, run, func(fresh *platformv1alpha1.SecurityToolRun) {
		fresh.Status.Phase = platformv1alpha1.SecurityToolRunPhaseFailed
		fresh.Status.Message = message
		if fresh.Status.Result == nil {
			fresh.Status.Result = &platformv1alpha1.SecurityToolRunResult{}
		}
		fresh.Status.Result.Status = resultStatus
		fresh.Status.Result.Errors = clampSecurityToolErrors(append(fresh.Status.Result.Errors, message))
		fresh.Status.CompletedAt = new(metav1.Now())
		setCondition(&fresh.Status.Conditions, "Ready", metav1.ConditionFalse, reason, message)
		setCondition(&fresh.Status.Conditions, "Completed", metav1.ConditionTrue, reason, message)
	}); err != nil {
		return err
	}
	// A cleanup that does not settle here is retried by the reconcile the
	// status update itself triggers, through the terminal-run path.
	if _, err := r.deleteStagedTarget(ctx, run); err != nil {
		return err
	}
	return nil
}

func (r *SecurityToolRunReconciler) patchStatus(ctx context.Context, run *platformv1alpha1.SecurityToolRun, mutate func(*platformv1alpha1.SecurityToolRun)) error {
	key := client.ObjectKeyFromObject(run)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.SecurityToolRun{}
		if err := r.Get(ctx, key, fresh); err != nil {
			return err
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		mutate(fresh)
		fresh.Status.ObservedGeneration = fresh.Generation
		return r.Status().Patch(ctx, fresh, patch)
	}); err != nil {
		return fmt.Errorf("updating SecurityToolRun status: %w", err)
	}
	return nil
}

func clampInt32(value int) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < 0 {
		return 0
	}
	return int32(value)
}

func (r *SecurityToolRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.SecurityToolRun{}).
		Owns(&batchv1.Job{}).
		Named("securitytoolrun").
		Complete(r)
}
