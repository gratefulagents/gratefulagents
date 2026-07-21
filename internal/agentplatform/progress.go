package agentplatform

import (
	"context"
	"fmt"
	"log"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	agentsandboxextensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BuildCRDClient creates a controller-runtime client that can read/write AgentRun status.
func BuildCRDClient() (client.WithWatch, error) {
	c, _, err := BuildCRDClientWithScheme()
	return c, err
}

// BuildCRDClientWithScheme creates a controller-runtime client plus the runtime scheme it uses.
func BuildCRDClientWithScheme() (client.WithWatch, *runtime.Scheme, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("getting in-cluster config: %w", err)
	}

	scheme, err := NewCRDScheme()
	if err != nil {
		return nil, nil, err
	}

	c, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("creating client: %w", err)
	}
	return c, scheme, nil
}

// NewCRDScheme builds the runtime scheme used by agent pods. It registers
// every API group agent-side tools read or write: platform CRDs, trigger CRDs
// (e.g. the GitHubRepository that maintainer tools consult for caps and the
// merge opt-in), agent-sandbox CRDs, and core types.
func NewCRDScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding platform scheme: %w", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding triggers scheme: %w", err)
	}
	if err := agentsandboxv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding agent-sandbox scheme: %w", err)
	}
	if err := agentsandboxextensionsv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding agent-sandbox extensions scheme: %w", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding core scheme: %w", err)
	}
	return scheme, nil
}

// BuildK8sClient creates a kubernetes client using in-cluster config.
func BuildK8sClient() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("getting in-cluster config: %w", err)
	}
	return kubernetes.NewForConfig(config)
}

// EmitEvent creates a Kubernetes Event associated with the given CRD object.
func EmitEvent(ctx context.Context, k8sClient *kubernetes.Clientset, taskName, taskNamespace, taskUID, crdKind, reportingController string, eventType, reason, message string) {
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", taskName),
			Namespace:    taskNamespace,
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: platformv1alpha1.GroupVersion.String(),
			Kind:       crdKind,
			Name:       taskName,
			Namespace:  taskNamespace,
			UID:        types.UID(taskUID),
		},
		Reason:              reason,
		Message:             message,
		Type:                eventType,
		EventTime:           metav1.NewMicroTime(time.Now()),
		ReportingController: reportingController,
		ReportingInstance:   taskName,
		Action:              reason,
	}

	if _, err := k8sClient.CoreV1().Events(taskNamespace).Create(ctx, event, metav1.CreateOptions{}); err != nil {
		log.Printf("WARN: failed to emit event: %v", err)
	}
}
