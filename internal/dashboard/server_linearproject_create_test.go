package dashboard

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func validCreateLinearProjectRequest() *platform.CreateLinearProjectRequest {
	return &platform.CreateLinearProjectRequest{
		Name: "linear-payments", LinearApiKey: "lin_api_key", ProjectId: "project-1", TeamId: "team-1",
		PollInterval: "45s", ApprovedLabel: "ready", AutoCreateTasks: true,
		Defaults: &platform.AgentRunDefaults{Model: "claude-sonnet-4-6", Provider: "anthropic", ClaudeApiKeySecret: "anthropic-key"},
	}
}

func TestCreateLinearProjectUsesPersonalNamespaceAndCreatesSecret(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewServer(c, scheme, nil, nil, false)
	ctx := triggerActorCtx("linear-user", "member")

	resp, err := srv.CreateLinearProject(ctx, validCreateLinearProjectRequest())
	if err != nil {
		t.Fatalf("CreateLinearProject() error = %v", err)
	}
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	if resp.Namespace != namespace {
		t.Fatalf("Namespace = %q, want %q", resp.Namespace, namespace)
	}
	lp := &triggersv1alpha1.LinearProject{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "linear-payments"}, lp); err != nil {
		t.Fatalf("Get(LinearProject) error = %v", err)
	}
	if lp.Spec.LinearAPIKeySecret != "linear-payments-linear-api-key" || lp.Spec.ProjectID != "project-1" || lp.Spec.TeamID != "team-1" {
		t.Fatalf("Linear wiring = %#v", lp.Spec)
	}
	if lp.Spec.PollInterval.Duration.String() != "45s" || lp.Spec.Defaults.Model != "claude-sonnet-4-6" {
		t.Fatalf("defaults/poll interval not persisted: %#v", lp.Spec)
	}
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: lp.Spec.LinearAPIKeySecret}, secret); err != nil {
		t.Fatalf("Get(Secret) error = %v", err)
	}
	if got := string(secret.Data["api-key"]); got != "lin_api_key" {
		t.Fatalf("Secret api-key = %q", got)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Name != lp.Name {
		t.Fatalf("Secret owner references = %#v", secret.OwnerReferences)
	}
}

func TestCreateLinearProjectValidatesInputs(t *testing.T) {
	scheme := testProjectScheme(t)
	srv := NewServer(fake.NewClientBuilder().WithScheme(scheme).Build(), scheme, nil, nil, false)
	ctx := triggerActorCtx("linear-user", "member")
	for name, mutate := range map[string]func(*platform.CreateLinearProjectRequest){
		"dns name":      func(req *platform.CreateLinearProjectRequest) { req.Name = "Not DNS" },
		"api key":       func(req *platform.CreateLinearProjectRequest) { req.LinearApiKey = "" },
		"project id":    func(req *platform.CreateLinearProjectRequest) { req.ProjectId = "" },
		"team id":       func(req *platform.CreateLinearProjectRequest) { req.TeamId = "" },
		"poll interval": func(req *platform.CreateLinearProjectRequest) { req.PollInterval = "0s" },
	} {
		t.Run(name, func(t *testing.T) {
			req := validCreateLinearProjectRequest()
			mutate(req)
			if _, err := srv.CreateLinearProject(ctx, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("error = %v, want InvalidArgument", err)
			}
		})
	}
}

func TestCreateLinearProjectRollsBackWhenSecretOwnershipFails(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.Secret); ok && len(obj.GetOwnerReferences()) != 0 {
				return errors.New("injected Secret ownership failure")
			}
			return cl.Update(ctx, obj, opts...)
		},
	}).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ctx := triggerActorCtx("linear-user", "member")
	_, err := srv.CreateLinearProject(ctx, validCreateLinearProjectRequest())
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("error = %v, want Internal", err)
	}
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "linear-payments"}, &triggersv1alpha1.LinearProject{}); !apierrors.IsNotFound(err) {
		t.Fatalf("LinearProject should have been rolled back, got %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "linear-payments-linear-api-key"}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Secret should have been rolled back, got %v", err)
	}
}

func TestCreateLinearProjectRollsBackSecretWhenTriggerCreateFails(t *testing.T) {
	scheme := testProjectScheme(t)
	failingClient := &createProjectFailingClient{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		failOnCreate: map[schema.GroupVersionKind]error{
			triggersv1alpha1.GroupVersion.WithKind("LinearProject"): apierrors.NewAlreadyExists(
				schema.GroupResource{Group: triggersv1alpha1.GroupVersion.Group, Resource: "linearprojects"}, "linear-payments"),
		},
	}
	srv := &Server{k8sClient: failingClient, scheme: scheme}
	ctx := triggerActorCtx("linear-user", "member")
	_, err := srv.CreateLinearProject(ctx, validCreateLinearProjectRequest())
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("error = %v, want AlreadyExists", err)
	}
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	secret := &corev1.Secret{}
	if err := failingClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "linear-payments-linear-api-key"}, secret); !apierrors.IsNotFound(err) {
		t.Fatalf("Secret should have been rolled back, got %v", err)
	}
}
