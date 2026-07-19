package triggers

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// slackWorkerInfraSecretName mirrors the platform package's per-namespace infra
// Secret name. The connector consumes DATABASE_URL from it, like worker pods.
const slackWorkerInfraSecretName = "gratefulagents-worker-infra"

// slackResourcePrefix prefixes connector-owned resource names.
const slackResourcePrefix = "slack"

// slackResourceName converts a SlackAgent name into a valid DNS-1123 label
// prefixed with "slack-", used for the connector's Deployment, ServiceAccount,
// and RBAC objects so names are predictable and collision-free.
func slackResourceName(name string) string {
	combined := strings.ToLower(strings.TrimSpace(slackResourcePrefix + "-" + name))
	var b strings.Builder
	prevDash := false
	for _, r := range combined {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = slackResourcePrefix
	}
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	if out == "" {
		out = slackResourcePrefix
	}
	return out
}

// slackSecretEnv builds an optional env var sourced from a key in the SlackAgent
// tokens Secret. Optional so a connector with only some tokens still starts (the
// connector validates required tokens itself).
func slackSecretEnv(envName, secretName, secretKey string) corev1.EnvVar {
	optional := true
	return corev1.EnvVar{
		Name: envName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  secretKey,
				Optional:             &optional,
			},
		},
	}
}

// workerInfraSecretEnvRef builds an optional env var sourced from the shared
// per-namespace worker infra Secret (e.g. DATABASE_URL).
func workerInfraSecretEnvRef(envName, secretKey string) corev1.EnvVar {
	optional := true
	return corev1.EnvVar{
		Name: envName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: slackWorkerInfraSecretName},
				Key:                  secretKey,
				Optional:             &optional,
			},
		},
	}
}

func intstrFromInt(port int) intstr.IntOrString {
	return intstr.FromInt32(int32(port))
}

// ensureSlackWorkerInfraSecret syncs the operator's infra credentials
// (DATABASE_URL, S3 keys) into a namespace so connectors read thread/draft
// state and the AgentRuns they create can reach object storage. Mirrors the
// per-namespace worker infra Secret used by run pods.
func ensureSlackWorkerInfraSecret(ctx context.Context, c client.Client, namespace string) error {
	data := map[string][]byte{}
	for _, kv := range []struct{ key, env string }{
		{"database-url", "DATABASE_URL"},
		{"aws-access-key-id", "AWS_ACCESS_KEY_ID"},
		{"aws-secret-access-key", "AWS_SECRET_ACCESS_KEY"},
	} {
		if v := strings.TrimSpace(os.Getenv(kv.env)); v != "" {
			data[kv.key] = []byte(v)
		}
	}
	if len(data) == 0 {
		return nil
	}
	return upsertSecretData(ctx, c, namespace, slackWorkerInfraSecretName, nil, data)
}

// upsertSecretData creates the Secret or merges data into the existing one.
func upsertSecretData(ctx context.Context, c client.Client, namespace, name string, labels map[string]string, data map[string][]byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Data:       data,
	}
	if err := c.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Secret %s/%s: %w", namespace, name, err)
		}
		existing := &corev1.Secret{}
		if getErr := c.Get(ctx, client.ObjectKeyFromObject(secret), existing); getErr != nil {
			return fmt.Errorf("getting Secret %s/%s: %w", namespace, name, getErr)
		}
		if existing.Data == nil {
			existing.Data = map[string][]byte{}
		}
		maps.Copy(existing.Data, data)
		if updateErr := c.Update(ctx, existing); updateErr != nil {
			return fmt.Errorf("updating Secret %s/%s: %w", namespace, name, updateErr)
		}
	}
	return nil
}

// buildSlackConnectorDeployment shapes a connector Deployment with the shared
// selector/label/owner plumbing used by both connector kinds.
func buildSlackConnectorDeployment(
	name, namespace, component, selectorKey, selectorValue string,
	ownerRef metav1.OwnerReference, replicas int32, podSpec corev1.PodSpec,
) *appsv1.Deployment {
	labels := map[string]string{
		"app.kubernetes.io/name":      "gratefulagents",
		"app.kubernetes.io/component": component,
		selectorKey:                   selectorValue,
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{selectorKey: selectorValue}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

// applySlackConnectorDeployment creates the Deployment or updates its labels,
// replicas, and pod template in place.
func applySlackConnectorDeployment(ctx context.Context, c client.Client, desired *appsv1.Deployment) error {
	existing := &appsv1.Deployment{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("getting connector Deployment: %w", err)
		}
		if createErr := c.Create(ctx, desired); createErr != nil && !apierrors.IsAlreadyExists(createErr) {
			return fmt.Errorf("creating connector Deployment: %w", createErr)
		}
		return nil
	}
	existing.Labels = desired.Labels
	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template = desired.Spec.Template
	if err := c.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating connector Deployment: %w", err)
	}
	return nil
}

// slackConnectorPodSpec assembles the shared connector pod shape: toolkit init
// container, `agent slack` entrypoint, health/readiness probes.
func slackConnectorPodSpec(saName, image string, env []corev1.EnvVar) corev1.PodSpec {
	probe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstrFromInt(8080)},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       15,
	}
	readiness := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstrFromInt(8080)},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       10,
	}
	return corev1.PodSpec{
		ServiceAccountName: saName,
		InitContainers: []corev1.Container{{
			Name:            "inject-toolkit",
			Image:           firstNonEmptyString(os.Getenv("INJECTOR_IMAGE"), "gratefulagents-injector:latest"),
			ImagePullPolicy: corev1.PullIfNotPresent,
			VolumeMounts:    []corev1.VolumeMount{{Name: "gratefulagents-toolkit", MountPath: "/shared"}},
		}},
		Containers: []corev1.Container{{
			Name:            "connector",
			Image:           firstNonEmptyString(image, os.Getenv("WORKER_IMAGE"), "worker:latest"),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"/opt/gratefulagents/bin/agent", "slack"},
			Env:             env,
			Ports:           []corev1.ContainerPort{{Name: "health", ContainerPort: 8080}},
			LivenessProbe:   probe,
			ReadinessProbe:  readiness,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "gratefulagents-toolkit", MountPath: "/opt/gratefulagents", SubPath: "gratefulagents", ReadOnly: true},
			},
		}},
		Volumes: []corev1.Volume{
			{Name: "gratefulagents-toolkit", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}
}

// slackDeploymentAvailability reports whether a connector Deployment has its
// desired replicas ready (false while suspended at zero replicas).
func slackDeploymentAvailability(ctx context.Context, c client.Client, namespace, name string) (bool, int32) {
	dep := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, dep); err != nil {
		return false, 0
	}
	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	if desired == 0 {
		return false, 0
	}
	return dep.Status.ReadyReplicas >= desired, dep.Status.ReadyReplicas
}

// slackWorkspaceResourceName names resources owned by a SlackWorkspace's
// connector, keeping them distinct from per-agent connector resources.
func slackWorkspaceResourceName(name string) string {
	return slackResourceName("ws-" + name)
}

// SlackWorkspaceBotSecretName is the per-member-namespace Secret carrying the
// shared workspace app's bot token (key: bot-token). The workspace controller
// syncs it into every member namespace so child AgentRuns can authenticate
// agent-side Slack read tools; the connector references it on the runs it
// creates. Exported because cmd/agent needs the same name.
func SlackWorkspaceBotSecretName(workspaceName string) string {
	return slackResourceName("ws-" + workspaceName + "-bot")
}
