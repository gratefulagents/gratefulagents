package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/computeruse"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var execComputerUse = execComputerUseInPod

func computerUseError(code connect.Code) error {
	return connect.NewError(code, errors.New("computer use unavailable"))
}

func (h *PlatformServiceConnectHandler) ExchangeComputerUse(ctx context.Context, req *connect.Request[platform.ExchangeComputerUseRequest]) (*connect.Response[platform.ExchangeComputerUseResponse], error) {
	response, err := h.srv.ExchangeComputerUse(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (s *Server) ExchangeComputerUse(ctx context.Context, req *platform.ExchangeComputerUseRequest) (*platform.ExchangeComputerUseResponse, error) {
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded || actor.Subject == "" {
		return nil, computerUseError(connect.CodeUnauthenticated)
	}
	if s.stateStore == nil || s.k8sClient == nil || actor.Role == "viewer" {
		return nil, computerUseError(connect.CodeNotFound)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.GetNamespace(), Name: req.GetName()}, run); err != nil {
		return nil, computerUseError(connect.CodeNotFound)
	}
	resourceType, resourceName := "agent_run", run.Name
	if project := agentRunProjectName(run); project != "" {
		resourceType, resourceName = projectResourceType, project
	}
	ownership, err := s.stateStore.GetResourceOwner(ctx, resourceType, resourceName, run.Namespace)
	if err != nil {
		return nil, computerUseError(connect.CodeNotFound)
	}
	if (ownership == nil || ownership.OwnerID == "") && resourceType == "agent_run" {
		if triggerType := agentRunTriggerResourceTypes[run.Spec.Trigger.Kind]; triggerType != "" && run.Spec.Trigger.Name != "" {
			ownership, err = s.stateStore.GetResourceOwner(ctx, triggerType, run.Spec.Trigger.Name, run.Namespace)
		}
	}
	if err != nil || ownership == nil || ownership.OwnerID == "" {
		return nil, computerUseError(connect.CodeNotFound)
	}
	if ownership.OwnerID != actor.Subject && actor.Role != "admin" && actor.Role != "owner" {
		return nil, computerUseError(connect.CodeNotFound)
	}
	exchange := computeruse.Exchange{Namespace: run.Namespace, Run: run.Name, Owner: actor.Subject, SessionID: req.GetSessionId(), Operation: req.GetOperation(), RequestID: req.GetRequestId()}
	if raw := req.GetOutcomeJson(); raw != "" {
		exchange.Outcome = &computeruse.Outcome{}
		if computeruse.Decode(bytes.NewBufferString(raw), exchange.Outcome) != nil {
			return nil, computerUseError(connect.CodeInvalidArgument)
		}
	}
	if exchange.Validate() != nil {
		return nil, computerUseError(connect.CodeInvalidArgument)
	}
	if exchange.Operation != "stop" && (isTerminalAgentRunPhase(run.Status.Phase) || !run.DeletionTimestamp.IsZero() || run.Annotations[cancelRequestedAnnotation] != "" || run.Annotations[promoteSucceededAnnotation] != "") {
		return nil, computerUseError(connect.CodeFailedPrecondition)
	}
	input, err := json.Marshal(exchange)
	if err != nil || len(input) > computeruse.MaxWire {
		return nil, computerUseError(connect.CodeInvalidArgument)
	}
	podName := agentRunPodName(run)
	if podName == "" || run.UID == "" || s.clientset == nil || s.restConfig == nil {
		return nil, computerUseError(connect.CodeFailedPrecondition)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pod, err := s.clientset.CoreV1().Pods(run.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil || !isPodOwnedByAgentRun(pod, run) {
		return nil, computerUseError(connect.CodeNotFound)
	}
	if exchange.Operation != "stop" && (!pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning) {
		return nil, computerUseError(connect.CodeFailedPrecondition)
	}
	output, err := execComputerUse(ctx, s.clientset, s.restConfig, pod.Name, run.Namespace, input)
	if err != nil {
		return nil, computerUseError(connect.CodeFailedPrecondition)
	}
	var response computeruse.Response
	if computeruse.Decode(bytes.NewReader(output), &response) != nil || response.Validate() != nil || response.Reason == "computer use request rejected" {
		return nil, computerUseError(connect.CodeFailedPrecondition)
	}
	// Re-encode the fixed response shape; never forward arbitrary process output.
	output, err = json.Marshal(response)
	if err != nil {
		return nil, computerUseError(connect.CodeFailedPrecondition)
	}
	return &platform.ExchangeComputerUseResponse{ResponseJson: string(output)}, nil
}

type computerUseBuffer struct {
	bytes.Buffer
	limit int
}

func (b *computerUseBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, computeruse.ErrRejected
	}
	return b.Buffer.Write(p)
}

func execComputerUseInPod(ctx context.Context, clientset *kubernetes.Clientset, config *rest.Config, pod, namespace string, input []byte) ([]byte, error) {
	if len(input) > computeruse.MaxWire {
		return nil, computeruse.ErrRejected
	}
	req := clientset.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).SubResource("exec").VersionedParams(&corev1.PodExecOptions{
		Container: agentRunWorkerContainerName,
		Command:   []string{"/opt/gratefulagents/bin/agent", "desktop-bridge"},
		Stdin:     true, Stdout: true, Stderr: true,
	}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return nil, computeruse.ErrRejected
	}
	stdout := &computerUseBuffer{limit: computeruse.MaxWire}
	stderr := &computerUseBuffer{limit: 4096}
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: bytes.NewReader(input), Stdout: stdout, Stderr: stderr}); err != nil {
		return nil, computeruse.ErrRejected
	}
	return stdout.Bytes(), nil
}
