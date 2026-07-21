package platform

import (
	"context"
	"errors"
	"reflect"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/sdk/pkg/agentsdk/sandbox"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	agentsandboxextensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func addSandboxSupportSchemes(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()
	if err := agentsandboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(agent-sandbox): %v", err)
	}
	if err := agentsandboxextensionsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(agent-sandbox extensions): %v", err)
	}
}

func TestRunRBACRulesLimitAgentRunAccessToCurrentRun(t *testing.T) {
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "current-run"}}
	rules := runRBACRules(run, "", "")
	assertRuleWithVerbAndNames(t, rules, "agentruns", "get", "current-run")
	assertRuleWithVerbAndNames(t, rules, "agentruns", "patch", "current-run")
	assertRuleWithVerbAndNames(t, rules, "agentruns/status", "get", "current-run")
	assertRuleWithVerbAndNames(t, rules, "agentruns/status", "patch", "current-run")
	assertRuleWithVerbAndNames(t, rules, "agentruns/status", "update", "current-run")
	assertNoResourceVerb(t, rules, "agentruns", "create")
	assertNoResourceVerb(t, rules, "agentruns", "list")
	assertHasRuleVerbs(t, rules, "platform.gratefulagents.dev", "mcpservers", "get")
	assertHasRuleVerbs(t, rules, "platform.gratefulagents.dev", "skills", "get")
}

func TestRunRBACRulesGiveMaintainerFleetAccess(t *testing.T) {
	controller := true
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: "repo-maintainer", Namespace: "default",
		Labels: map[string]string{
			orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleMaintainer,
			orchestration.SupervisedRunLabel:   "repo",
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "GitHubRepository", Name: "repo", Controller: &controller,
		}},
	}}
	if namespace, name := maintainedRepositoryForRun(run); namespace != "default" || name != "repo" {
		t.Fatalf("maintainedRepositoryForRun() = %q/%q, want default/repo", namespace, name)
	}
	got := envSliceValueMap(runtimeScopeEnvs(run))
	assertEnvValue(t, got, "AGENTRUN_MAINTAINED_REPOSITORY_NAME", "repo")
	assertEnvValue(t, got, "AGENTRUN_MAINTAINED_REPOSITORY_NAMESPACE", "default")

	rules := runRBACRules(run, "", "repo")
	fleetAccess := false
	for _, rule := range rules {
		if contains(rule.APIGroups, "platform.gratefulagents.dev") && contains(rule.Resources, "agentruns") && len(rule.ResourceNames) == 0 && contains(rule.Verbs, "get") && contains(rule.Verbs, "list") && contains(rule.Verbs, "watch") && contains(rule.Verbs, "patch") && contains(rule.Verbs, "update") {
			fleetAccess = true
		}
	}
	if !fleetAccess {
		t.Fatalf("missing namespace-wide fleet access in %#v", rules)
	}
	assertHasRuleVerbs(t, rules, "platform.gratefulagents.dev", "modetemplates", "get", "list")
	assertHasRuleVerbs(t, rules, "triggers.gratefulagents.dev", "maintainerworkitems", "get", "list", "watch")
	assertHasRuleVerbs(t, rules, "triggers.gratefulagents.dev", "maintainerworkitemcommands", "create", "get", "list", "watch")
	assertRuleWithGroupVerbAndNames(t, rules, "", "secrets", "get", triggersv1alpha1.MaintainerCommandCapabilitySecretName(run.Name))
	assertNoResourceVerb(t, rules, "maintainerworkitems", "patch")
	assertNoResourceVerb(t, rules, "maintainerworkitemcommands", "update")
	for _, rule := range rules {
		if contains(rule.APIGroups, "triggers.gratefulagents.dev") && contains(rule.Resources, "githubrepositories") {
			if !contains(rule.ResourceNames, "repo") || !contains(rule.Verbs, "get") {
				t.Fatalf("GitHubRepository rule = %#v, want get scoped to repo", rule)
			}
			return
		}
	}
	t.Fatal("missing scoped GitHubRepository rule")
}

func TestEnsureMaintainerCommandCapabilityIsPrivateAndStable(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := maintainerRunForRBAC("repo-maintainer", "repo")
	repository := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: "default", UID: types.UID("repo-uid")}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, repository).Build()
	controller := true
	owner := metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: run.Name, UID: run.UID, Controller: &controller}
	if err := ensureMaintainerCommandCapability(context.Background(), c, run, owner, repository.Name); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: run.Namespace, Name: triggersv1alpha1.MaintainerCommandCapabilitySecretName(run.Name)}
	if err := c.Get(context.Background(), key, secret); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), secret.Data[triggersv1alpha1.MaintainerCommandCapabilitySecretKey]...)
	if len(first) != 32 || !metav1.IsControlledBy(secret, run) || secret.Immutable == nil || !*secret.Immutable || string(secret.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryNameKey]) != repository.Name || string(secret.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryUIDKey]) != string(repository.UID) {
		t.Fatalf("capability Secret = %#v", secret)
	}
	if err := ensureMaintainerCommandCapability(context.Background(), c, run, owner, repository.Name); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), key, secret); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, secret.Data[triggersv1alpha1.MaintainerCommandCapabilitySecretKey]) {
		t.Fatal("capability changed across idempotent reconciliation")
	}
}

func maintainerRunForRBAC(name, repository string) *platformv1alpha1.AgentRun {
	controller := true
	return &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "default", UID: types.UID(name + "-uid"),
		Labels:          map[string]string{orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleMaintainer, orchestration.SupervisedRunLabel: repository},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "GitHubRepository", Name: repository, UID: types.UID(repository + "-uid"), Controller: &controller}},
	}}
}

func TestRunRBACRulesGiveOverseerReadOnlyAccessToSupervisedRun(t *testing.T) {
	controller := true
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: "primary-overseer", Namespace: "default",
		Labels: map[string]string{
			orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleOverseer,
			orchestration.SupervisedRunLabel:   "primary",
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: "primary", UID: types.UID("primary-uid"), Controller: &controller,
		}},
	}}
	rules := runRBACRules(run, "primary", "")
	assertRuleWithVerbAndNames(t, rules, "agentruns", "get", "primary-overseer", "primary")
	assertRuleWithVerbAndNames(t, rules, "agentruns", "patch", "primary-overseer")
	for _, rule := range rules {
		if contains(rule.Resources, "agentruns") && contains(rule.Verbs, "patch") && contains(rule.ResourceNames, "primary") {
			t.Fatalf("overseer may patch supervised AgentRun: %#v", rule)
		}
	}
}

// The zero-trust read-only fallback in the run pod depends on reading these
// namespaced CRDs from the run's own namespace. They must be granted by the
// per-run namespaced Role — not only by the shared agent-reader ClusterRole,
// whose rules are rewritten by whichever operator binary last reconciled a
// run and whose per-run bindings are deleted on terminal transitions.
func TestRunRBACRulesGrantNamespacedPolicyReads(t *testing.T) {
	rules := runRBACRules(&platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run"}}, "", "")
	assertHasRuleVerbs(t, rules, "platform.gratefulagents.dev", "runtimeprofiles", "get")
	assertHasRuleVerbs(t, rules, "platform.gratefulagents.dev", "mcppolicies", "get")
	assertHasRuleVerbs(t, rules, "platform.gratefulagents.dev", "guardrailpolicies", "get")
}

func TestEnsureRunRBACUpdatesExistingRunRoleRules(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(rbac): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-with-skills",
			Namespace: "default",
			UID:       types.UID("run-with-skills-uid"),
		},
	}
	saName := "run-run-with-skills"
	existingRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: saName + "-role", Namespace: run.Namespace},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{"platform.gratefulagents.dev"},
			Resources: []string{"agentruns"},
			Verbs:     []string{"get"},
		}},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, existingRole).Build()
	if err := ensureRunRBAC(context.Background(), c, run, saName); err != nil {
		t.Fatalf("ensureRunRBAC() error = %v", err)
	}

	updated := &rbacv1.Role{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: saName + "-role", Namespace: run.Namespace}, updated); err != nil {
		t.Fatalf("Get(Role) error = %v", err)
	}
	assertHasRuleVerbs(t, updated.Rules, "platform.gratefulagents.dev", "mcpservers", "get")
	assertHasRuleVerbs(t, updated.Rules, "platform.gratefulagents.dev", "skills", "get")
	assertHasRuleVerbs(t, updated.Rules, "platform.gratefulagents.dev", "runtimeprofiles", "get")
	assertHasRuleVerbs(t, updated.Rules, "platform.gratefulagents.dev", "mcppolicies", "get")
	assertHasRuleVerbs(t, updated.Rules, "platform.gratefulagents.dev", "guardrailpolicies", "get")
}

func assertHasRuleVerbs(t *testing.T, rules []rbacv1.PolicyRule, apiGroup, resource string, wantVerbs ...string) {
	t.Helper()
	for _, rule := range rules {
		if contains(rule.APIGroups, apiGroup) && contains(rule.Resources, resource) {
			for _, verb := range wantVerbs {
				if !contains(rule.Verbs, verb) {
					t.Fatalf("rule %s/%s missing verb %q; verbs=%v", apiGroup, resource, verb, rule.Verbs)
				}
			}
			return
		}
	}
	t.Fatalf("missing rule for %s/%s", apiGroup, resource)
}

func assertRuleWithVerbAndNames(t *testing.T, rules []rbacv1.PolicyRule, resource, verb string, names ...string) {
	t.Helper()
	assertRuleWithGroupVerbAndNames(t, rules, "platform.gratefulagents.dev", resource, verb, names...)
}

func assertRuleWithGroupVerbAndNames(t *testing.T, rules []rbacv1.PolicyRule, apiGroup, resource, verb string, names ...string) {
	t.Helper()
	for _, rule := range rules {
		if !contains(rule.APIGroups, apiGroup) || !contains(rule.Resources, resource) || !contains(rule.Verbs, verb) {
			continue
		}
		for _, name := range names {
			if !contains(rule.ResourceNames, name) {
				t.Fatalf("rule %s/%s missing resourceName %q; names=%v", resource, verb, name, rule.ResourceNames)
			}
		}
		return
	}
	t.Fatalf("missing rule for %s verb %s", resource, verb)
}

func assertNoResourceVerb(t *testing.T, rules []rbacv1.PolicyRule, resource, verb string) {
	t.Helper()
	for _, rule := range rules {
		if contains(rule.Resources, resource) && contains(rule.Verbs, verb) {
			t.Fatalf("unexpected %s verb %s in rule %#v", resource, verb, rule)
		}
	}
}

func contains(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

func TestEnsureClusterScopedRBACCreatesAdminBindingForKubernetesAdminRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(rbac): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "admin-run", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{KubernetesAdmin: true},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	if err := ensureClusterScopedRBAC(context.Background(), c, run, "run-admin-run"); err != nil {
		t.Fatalf("ensureClusterScopedRBAC() error = %v", err)
	}

	adminBinding := &rbacv1.ClusterRoleBinding{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "run-admin-run-admin-binding"}, adminBinding); err != nil {
		t.Fatalf("Get(admin ClusterRoleBinding) error = %v", err)
	}
	if adminBinding.RoleRef.Kind != "ClusterRole" || adminBinding.RoleRef.Name != "cluster-admin" {
		t.Fatalf("RoleRef = %#v, want ClusterRole cluster-admin", adminBinding.RoleRef)
	}
	if len(adminBinding.Subjects) != 1 || adminBinding.Subjects[0].Name != "run-admin-run" || adminBinding.Subjects[0].Namespace != "default" {
		t.Fatalf("Subjects = %#v, want run service account", adminBinding.Subjects)
	}
	if adminBinding.Labels["platform.gratefulagents.dev/owner-run"] != "admin-run" || adminBinding.Labels["platform.gratefulagents.dev/namespace"] != "default" {
		t.Fatalf("labels = %#v, want owner-run/namespace cleanup labels", adminBinding.Labels)
	}
}

func TestEnsureClusterScopedRBACDeletesAdminBindingWhenFlagDisabled(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(rbac): %v", err)
	}

	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "admin-run", Namespace: "default"}}
	stale := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{
		Name: "run-admin-run-admin-binding",
		Labels: map[string]string{
			"platform.gratefulagents.dev/owner-run": "admin-run",
			"platform.gratefulagents.dev/namespace": "default",
		},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build()
	if err := ensureClusterScopedRBAC(context.Background(), c, run, "run-admin-run"); err != nil {
		t.Fatalf("ensureClusterScopedRBAC() error = %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "run-admin-run-admin-binding"}, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("admin binding lookup err = %v, want NotFound", err)
	}
}

func TestCreateExecutePodUsesUnifiedRunCommand(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(rbac): %v", err)
	}
	addSandboxSupportSchemes(t, scheme)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-chat",
			Namespace: "default",
			UID:       types.UID("run-chat-uid"),
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			WorkflowMode: platformv1alpha1.WorkflowModeAuto,
			Model:        "gpt-5.4",
			Image:        "ghcr.io/example/worker:latest",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	podName, err := createPlanPod(context.Background(), c, run)
	if err != nil {
		t.Fatalf("createPlanPod() error = %v", err)
	}
	if podName == "" {
		t.Fatal("createPlanPod() podName = empty, want created pod name")
	}

	pods := &corev1.PodList{}
	if err := c.List(context.Background(), pods); err != nil {
		t.Fatalf("List(Pods) error = %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("len(Pods) = %d, want 1 unified run pod", len(pods.Items))
	}
	pod := pods.Items[0]
	if len(pod.Spec.Containers) == 0 || len(pod.Spec.Containers[0].Command) < 2 || pod.Spec.Containers[0].Command[1] != "run" {
		t.Fatalf("pod command = %#v, want unified agent run", pod.Spec.Containers[0].Command)
	}
}

func TestCreateRunPodForcesNonRootWorker(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(rbac): %v", err)
	}
	addSandboxSupportSchemes(t, scheme)

	// elixir:1.17 is a real-world custom image that defaults to uid 0; the
	// forced non-root SecurityContext is what keeps the required bwrap
	// sandbox working (unprivileged user-namespace path needs uid != 0).
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-elixir",
			Namespace: "default",
			UID:       types.UID("run-elixir-uid"),
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			WorkflowMode: platformv1alpha1.WorkflowModeAuto,
			Model:        "gpt-5.4",
			Image:        "elixir:1.17",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	if _, err := createPlanPod(context.Background(), c, run); err != nil {
		t.Fatalf("createPlanPod() error = %v", err)
	}

	pods := &corev1.PodList{}
	if err := c.List(context.Background(), pods); err != nil {
		t.Fatalf("List(Pods) error = %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("len(Pods) = %d, want 1 run pod", len(pods.Items))
	}
	pod := pods.Items[0]

	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("len(Containers) = %d, want 1 worker container", len(pod.Spec.Containers))
	}
	worker := pod.Spec.Containers[0]
	sc := worker.SecurityContext
	if sc == nil {
		t.Fatal("worker SecurityContext = nil, want forced non-root context")
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != workerRunAsUID {
		t.Errorf("worker RunAsUser = %v, want %d", sc.RunAsUser, workerRunAsUID)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != workerRunAsGID {
		t.Errorf("worker RunAsGroup = %v, want %d", sc.RunAsGroup, workerRunAsGID)
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Errorf("worker RunAsNonRoot = %v, want true", sc.RunAsNonRoot)
	}
	if sc.ProcMount != nil {
		t.Errorf("worker ProcMount = %q, want default when private procfs is not enabled", *sc.ProcMount)
	}
	if pod.Spec.HostUsers != nil {
		t.Errorf("pod HostUsers = %v, want unset when private procfs is not enabled", *pod.Spec.HostUsers)
	}

	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("len(InitContainers) = %d, want 1 inject-toolkit container", len(pod.Spec.InitContainers))
	}
	// inject-toolkit must stay root: its cp -a entrypoint preserves toolkit
	// file ownership when copying into the shared volume.
	if init := pod.Spec.InitContainers[0]; init.SecurityContext != nil {
		t.Errorf("init container %q SecurityContext = %#v, want nil (must stay root)", init.Name, init.SecurityContext)
	}
	if pod.Spec.SecurityContext != nil {
		t.Errorf("pod-level SecurityContext = %#v, want nil (worker container-level only)", pod.Spec.SecurityContext)
	}
}

func TestCreatePlanPodReplacesCompletedExistingPod(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(rbac): %v", err)
	}
	addSandboxSupportSchemes(t, scheme)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "resume-plan",
			Namespace: "default",
			UID:       types.UID("resume-plan-uid"),
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			WorkflowMode: platformv1alpha1.WorkflowModeAuto,
			Model:        "gpt-5.4",
			Image:        "ghcr.io/example/worker:latest",
		},
	}
	podName := sanitizeDNSLabel("run", run.Name)
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "default",
			Labels: map[string]string{
				"platform.gratefulagents.dev/owner-run": run.Name,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: platformv1alpha1.GroupVersion.String(),
				Kind:       "AgentRun",
				Name:       run.Name,
				UID:        run.UID,
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing).
		Build()

	_, err := createPlanPod(context.Background(), c, run)
	if !errors.Is(err, errRunPodReplaced) {
		t.Fatalf("createPlanPod() error = %v, want errRunPodReplaced", err)
	}
	check := &corev1.Pod{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: podName, Namespace: "default"}, check); !apierrors.IsNotFound(err) {
		t.Fatalf("expected stale pod to be deleted before retry (err=%v)", err)
	}

	createdName, err := createPlanPod(context.Background(), c, run)
	if err != nil {
		t.Fatalf("createPlanPod() second call error = %v", err)
	}
	if createdName != podName {
		t.Fatalf("podName = %q, want %q", createdName, podName)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: podName, Namespace: "default"}, check); err != nil {
		t.Fatalf("get recreated pod: %v", err)
	}
}

func TestRuntimeScopeEnvsForRootRunUsesCurrentAsParent(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plan-root",
			Namespace: "engg",
			UID:       types.UID("uid-root"),
		},
	}

	envs := runtimeScopeEnvs(run)
	got := map[string]string{}
	for _, env := range envs {
		got[env.Name] = env.Value
	}

	assertEnvValue(t, got, "AGENTRUN_CURRENT_NAMESPACE", "engg")
	assertEnvValue(t, got, "AGENTRUN_CURRENT_NAME", "plan-root")
	assertEnvValue(t, got, "AGENTRUN_CURRENT_UID", "uid-root")
	assertEnvValue(t, got, "AGENTRUN_PARENT_NAMESPACE", "engg")
	assertEnvValue(t, got, "AGENTRUN_PARENT_NAME", "plan-root")
	assertEnvValue(t, got, "AGENTRUN_PARENT_UID", "uid-root")
	assertEnvValue(t, got, "RUN_NAMESPACE", "engg")
	assertEnvValue(t, got, "RUN_NAME", "plan-root")
	assertEnvValue(t, got, "RUN_UID", "uid-root")
}

func TestRuntimeScopeEnvsForChildRunUsesAgentRunOwnerRef(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-worker-a",
			Namespace: "engg",
			UID:       types.UID("uid-child"),
			Labels: map[string]string{
				teamParentLabelName: "plan-parent-fallback",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: platformv1alpha1.GroupVersion.String(),
					Kind:       "AgentRun",
					Name:       "plan-parent",
					UID:        types.UID("uid-parent"),
				},
			},
		},
	}

	envs := runtimeScopeEnvs(run)
	got := map[string]string{}
	for _, env := range envs {
		got[env.Name] = env.Value
	}

	assertEnvValue(t, got, "AGENTRUN_CURRENT_NAME", "child-worker-a")
	assertEnvValue(t, got, "AGENTRUN_PARENT_NAMESPACE", "engg")
	assertEnvValue(t, got, "AGENTRUN_PARENT_NAME", "plan-parent")
	assertEnvValue(t, got, "AGENTRUN_PARENT_UID", "uid-parent")
	assertEnvValue(t, got, "RUN_NAME", "plan-parent")
}

func TestRuntimeScopeEnvsForOverseerIncludesValidatedSupervisedIdentity(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "primary-overseer",
			Namespace: "engg",
			UID:       types.UID("uid-overseer"),
			Labels: map[string]string{
				orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleOverseer,
				orchestration.SupervisedRunLabel:   "primary",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: platformv1alpha1.GroupVersion.String(),
				Kind:       "AgentRun",
				Name:       "primary",
				UID:        types.UID("uid-primary"),
			}},
		},
	}

	got := envSliceValueMap(runtimeScopeEnvs(run))
	assertEnvValue(t, got, "AGENTRUN_SUPERVISED_NAMESPACE", "engg")
	assertEnvValue(t, got, "AGENTRUN_SUPERVISED_NAME", "primary")
	assertEnvValue(t, got, "AGENTRUN_SUPERVISED_UID", "uid-primary")

	run.OwnerReferences[0].Name = "different-primary"
	got = envSliceValueMap(runtimeScopeEnvs(run))
	if _, ok := got["AGENTRUN_SUPERVISED_NAME"]; ok {
		t.Fatalf("mismatched owner must not expose supervised identity: %#v", got)
	}
}

func TestBuildCommonPodSpecOpenAIOAuthWiring(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Model:      "gpt-5.3-codex",
			AuthMode:   platformv1alpha1.AgentRunAuthModeOAuth,
			Secrets:    &platformv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "openai-oauth"},
		},
	}

	spec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
	envByName := map[string]corev1.EnvVar{}
	for _, env := range spec.Containers[0].Env {
		envByName[env.Name] = env
	}

	assertEnvValue(t, envValueMap(envByName), "OPENAI_AUTH_MODE", "oauth")
	assertEnvValue(t, envValueMap(envByName), "OPENAI_OAUTH_AUTH_JSON_PATH", openAIOAuthAuthJSONPath)
	assertEnvValue(t, envValueMap(envByName), "OPENAI_OAUTH_ACCOUNT_ID_PATH", openAIOAuthAccountIDPath)
	if _, ok := envByName["OPENAI_API_KEY"]; ok {
		t.Fatalf("OPENAI_API_KEY should be omitted in oauth mode, env=%#v", envByName["OPENAI_API_KEY"])
	}

	if !hasVolumeMount(spec.Containers[0].VolumeMounts, openAIOAuthVolumeName, openAIOAuthMountPath) {
		t.Fatalf("expected oauth volume mount %q at %q", openAIOAuthVolumeName, openAIOAuthMountPath)
	}
	if !hasSecretVolume(spec.Volumes, openAIOAuthVolumeName, "openai-oauth") {
		t.Fatalf("expected oauth secret volume %q with secret openai-oauth", openAIOAuthVolumeName)
	}
	if !secretVolumeProjectsKey(spec.Volumes, openAIOAuthVolumeName, "auth.json") {
		t.Fatalf("expected oauth volume %q to project auth.json", openAIOAuthVolumeName)
	}
	if !secretVolumeProjectsKey(spec.Volumes, openAIOAuthVolumeName, "account-id") {
		t.Fatalf("expected oauth volume %q to project account-id optionally", openAIOAuthVolumeName)
	}
	assertProjectedOptionalAccountID(t, spec.Volumes)
}

func TestBuildCommonPodSpecAnthropicOAuthWiring(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Model:      "anthropic/claude-sonnet-4-6",
			AuthMode:   platformv1alpha1.AgentRunAuthModeOAuth,
			Secrets: &platformv1alpha1.AgentRunSecrets{
				ClaudeAPIKeySecret: "anthropic-key",
				OpenAIOAuthSecret:  "anthropic-oauth",
				ProviderKeys: []platformv1alpha1.ProviderKeyRef{
					{Provider: "anthropic", SecretName: "anthropic-provider-key"},
					{Provider: "openrouter", SecretName: "openrouter-provider-key"},
				},
			},
		},
	}

	spec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
	envByName := map[string]corev1.EnvVar{}
	for _, env := range spec.Containers[0].Env {
		envByName[env.Name] = env
	}

	values := envValueMap(envByName)
	assertEnvValue(t, values, "OPENAI_AUTH_MODE", "oauth")
	assertEnvValue(t, values, anthropicOAuthAuthJSONEnv, openAIOAuthAuthJSONPath)
	if _, ok := envByName["ANTHROPIC_API_KEY"]; ok {
		t.Fatalf("ANTHROPIC_API_KEY should be omitted in oauth mode, env=%#v", envByName["ANTHROPIC_API_KEY"])
	}
	if _, ok := envByName["OPENROUTER_API_KEY"]; !ok {
		t.Fatalf("OPENROUTER_API_KEY should still be mounted for fallback providers")
	}
	if _, ok := envByName["OPENAI_OAUTH_AUTH_JSON_PATH"]; ok {
		t.Fatalf("OPENAI_OAUTH_AUTH_JSON_PATH should be omitted for Anthropic OAuth")
	}
	if !hasVolumeMount(spec.Containers[0].VolumeMounts, openAIOAuthVolumeName, openAIOAuthMountPath) {
		t.Fatalf("expected oauth volume mount %q at %q", openAIOAuthVolumeName, openAIOAuthMountPath)
	}
	if !hasSecretVolume(spec.Volumes, openAIOAuthVolumeName, "anthropic-oauth") {
		t.Fatalf("expected oauth secret volume %q with secret anthropic-oauth", openAIOAuthVolumeName)
	}
	if !secretVolumeProjectsKey(spec.Volumes, openAIOAuthVolumeName, "auth.json") {
		t.Fatalf("expected oauth volume %q to project auth.json", openAIOAuthVolumeName)
	}
}

func TestBuildCommonPodSpecMountsAdditionalProviderOAuthSecrets(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Model:      "anthropic/claude-sonnet-4-6",
			AuthMode:   platformv1alpha1.AgentRunAuthModeOAuth,
			Secrets: &platformv1alpha1.AgentRunSecrets{
				OpenAIOAuthSecret: "usercred-anthropic",
				ProviderOAuthSecrets: []platformv1alpha1.ProviderOAuthSecretRef{
					{Provider: "anthropic", SecretName: "usercred-anthropic"}, // served by the legacy volume — skipped
					{Provider: "openai", SecretName: "usercred-openai"},
					{Provider: "copilot", SecretName: "usercred-copilot"},
					{Provider: "gemini", SecretName: "usercred-gemini"}, // not OAuth-capable — skipped
				},
			},
		},
	}

	spec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
	envByName := map[string]corev1.EnvVar{}
	for _, env := range spec.Containers[0].Env {
		if _, dup := envByName[env.Name]; dup {
			t.Fatalf("duplicate env %q", env.Name)
		}
		envByName[env.Name] = env
	}
	values := envValueMap(envByName)

	// The run's own provider keeps the legacy single-volume wiring.
	assertEnvValue(t, values, anthropicOAuthAuthJSONEnv, openAIOAuthAuthJSONPath)
	if !hasSecretVolume(spec.Volumes, openAIOAuthVolumeName, "usercred-anthropic") {
		t.Fatalf("expected legacy oauth volume %q with secret usercred-anthropic", openAIOAuthVolumeName)
	}

	// Additional providers get their own mounts + path envs.
	assertEnvValue(t, values, "OPENAI_OAUTH_AUTH_JSON_PATH", "/var/run/gratefulagents/oauth/openai/auth.json")
	assertEnvValue(t, values, "OPENAI_OAUTH_ACCOUNT_ID_PATH", "/var/run/gratefulagents/oauth/openai/account-id")
	assertEnvValue(t, values, copilotOAuthAuthJSONEnv, "/var/run/gratefulagents/oauth/copilot/auth.json")
	if !hasVolumeMount(spec.Containers[0].VolumeMounts, "provider-oauth-openai", "/var/run/gratefulagents/oauth/openai") {
		t.Fatalf("expected provider-oauth-openai volume mount")
	}
	if !hasSecretVolume(spec.Volumes, "provider-oauth-openai", "usercred-openai") {
		t.Fatalf("expected provider-oauth-openai volume with secret usercred-openai")
	}
	if !secretVolumeProjectsKey(spec.Volumes, "provider-oauth-openai", "auth.json") {
		t.Fatalf("expected provider-oauth-openai volume to project auth.json")
	}
	if !hasVolumeMount(spec.Containers[0].VolumeMounts, "provider-oauth-copilot", "/var/run/gratefulagents/oauth/copilot") {
		t.Fatalf("expected provider-oauth-copilot volume mount")
	}
	if !hasSecretVolume(spec.Volumes, "provider-oauth-copilot", "usercred-copilot") {
		t.Fatalf("expected provider-oauth-copilot volume with secret usercred-copilot")
	}
	for _, v := range spec.Volumes {
		if v.Name == "provider-oauth-anthropic" {
			t.Fatalf("anthropic list entry must be skipped (legacy volume already serves it)")
		}
		if v.Name == "provider-oauth-gemini" {
			t.Fatalf("non-OAuth-capable providers must be skipped")
		}
	}
}

func TestBuildCommonPodSpecCopilotOAuthWiring(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Model:      "copilot/gpt-4.1",
			AuthMode:   platformv1alpha1.AgentRunAuthModeOAuth,
			Secrets: &platformv1alpha1.AgentRunSecrets{
				OpenAIOAuthSecret: "copilot-oauth",
				ProviderKeys: []platformv1alpha1.ProviderKeyRef{
					{Provider: "copilot", SecretName: "copilot-provider-key"},
					{Provider: "openrouter", SecretName: "openrouter-provider-key"},
				},
			},
		},
	}

	spec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
	envByName := map[string]corev1.EnvVar{}
	for _, env := range spec.Containers[0].Env {
		envByName[env.Name] = env
	}

	values := envValueMap(envByName)
	assertEnvValue(t, values, "OPENAI_AUTH_MODE", "oauth")
	assertEnvValue(t, values, copilotOAuthAuthJSONEnv, openAIOAuthAuthJSONPath)
	if _, ok := envByName["COPILOT_API_KEY"]; ok {
		t.Fatalf("COPILOT_API_KEY should be omitted in oauth mode, env=%#v", envByName["COPILOT_API_KEY"])
	}
	if _, ok := envByName["OPENROUTER_API_KEY"]; !ok {
		t.Fatalf("OPENROUTER_API_KEY should still be mounted for fallback providers")
	}
	if _, ok := envByName["OPENAI_OAUTH_AUTH_JSON_PATH"]; ok {
		t.Fatalf("OPENAI_OAUTH_AUTH_JSON_PATH should be omitted for Copilot OAuth")
	}
	if !hasVolumeMount(spec.Containers[0].VolumeMounts, openAIOAuthVolumeName, openAIOAuthMountPath) {
		t.Fatalf("expected oauth volume mount %q at %q", openAIOAuthVolumeName, openAIOAuthMountPath)
	}
	if !hasSecretVolume(spec.Volumes, openAIOAuthVolumeName, "copilot-oauth") {
		t.Fatalf("expected oauth secret volume %q with secret copilot-oauth", openAIOAuthVolumeName)
	}
	if !secretVolumeProjectsKey(spec.Volumes, openAIOAuthVolumeName, "auth.json") {
		t.Fatalf("expected oauth volume %q to project auth.json", openAIOAuthVolumeName)
	}
}

func TestBuildCommonPodSpecRequiresSubprocessSandbox(t *testing.T) {
	t.Setenv(sandbox.SandboxPathPrependEnv, "/opt/conda/bin")
	t.Setenv(sandbox.SandboxExtraReadOnlyPathsEnv, "/opt/conda")
	t.Setenv(sandbox.SandboxExtraEnvEnv, "JAVA_HOME=/opt/java")

	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Model:      "gpt-5.4",
		},
	}

	spec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
	envByName := map[string]corev1.EnvVar{}
	for _, env := range spec.Containers[0].Env {
		envByName[env.Name] = env
	}

	assertEnvValue(t, envValueMap(envByName), "GRATEFULAGENTS_COMMAND_SANDBOX", "required")
	assertEnvValue(t, envValueMap(envByName), sandbox.SandboxPathPrependEnv, "/opt/conda/bin")
	assertEnvValue(t, envValueMap(envByName), sandbox.SandboxExtraReadOnlyPathsEnv, "/opt/conda")
	assertEnvValue(t, envValueMap(envByName), sandbox.SandboxExtraWritablePathsEnv, workspaceScratchPath)
	assertEnvValue(t, envValueMap(envByName), sandbox.SandboxExtraEnvEnv,
		"JAVA_HOME=/opt/java\nGOPATH="+workspaceScratchPath+"/go\n"+
			"GOMODCACHE="+workspaceScratchPath+"/go/pkg/mod\nGOCACHE="+workspaceScratchPath+"/go-build")
	assertEnvValue(t, envValueMap(envByName), "GOPATH", workspaceScratchPath+"/go")
	assertEnvValue(t, envValueMap(envByName), "GOMODCACHE", workspaceScratchPath+"/go/pkg/mod")
	assertEnvValue(t, envValueMap(envByName), "GOCACHE", workspaceScratchPath+"/go-build")

	// Verify the SDK boundary consumes the exact controller-rendered contract.
	t.Setenv(sandbox.SandboxExtraWritablePathsEnv, envByName[sandbox.SandboxExtraWritablePathsEnv].Value)
	t.Setenv(sandbox.SandboxExtraEnvEnv, envByName[sandbox.SandboxExtraEnvEnv].Value)
	sandboxConfig := sandbox.ConfigFromEnv()
	if len(sandboxConfig.ExtraWritablePaths) != 1 || sandboxConfig.ExtraWritablePaths[0] != workspaceScratchPath {
		t.Fatalf("sandbox writable paths = %#v, want only %q", sandboxConfig.ExtraWritablePaths, workspaceScratchPath)
	}
	for key, want := range map[string]string{
		"GOPATH":     workspaceScratchPath + "/go",
		"GOMODCACHE": workspaceScratchPath + "/go/pkg/mod",
		"GOCACHE":    workspaceScratchPath + "/go-build",
	} {
		if got := sandboxConfig.ExtraEnv[key]; got != want {
			t.Fatalf("sandbox extra env %s = %q, want %q", key, got, want)
		}
	}
	if !hasVolumeMount(spec.Containers[0].VolumeMounts, workspaceScratchVolumeName, workspaceScratchPath) {
		t.Fatalf("expected scratch volume mount %q at %q", workspaceScratchVolumeName, workspaceScratchPath)
	}
	scratchVolumeFound := false
	for _, volume := range spec.Volumes {
		if volume.Name == workspaceScratchVolumeName && volume.EmptyDir != nil {
			scratchVolumeFound = true
			break
		}
	}
	if !scratchVolumeFound {
		t.Fatalf("expected scratch EmptyDir volume %q", workspaceScratchVolumeName)
	}
	if spec.ShareProcessNamespace == nil || *spec.ShareProcessNamespace {
		t.Fatalf("ShareProcessNamespace = %#v, want explicit false", spec.ShareProcessNamespace)
	}
	// The agent needs the grace window to push a final workspace WIP
	// snapshot when the pause/wake flow deletes the pod.
	if spec.TerminationGracePeriodSeconds == nil || *spec.TerminationGracePeriodSeconds != 60 {
		t.Fatalf("TerminationGracePeriodSeconds = %#v, want 60", spec.TerminationGracePeriodSeconds)
	}
}

func TestApplyRuntimeProfileSandboxOverridesPreservesScratchWithWorkspacePVC(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Model:      "gpt-5.4",
		},
	}
	podSpec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
	profile := &platformv1alpha1.RuntimeProfile{
		Spec: platformv1alpha1.RuntimeProfileSpec{Sandbox: &platformv1alpha1.RuntimeProfileSandbox{}},
	}
	applyRuntimeProfileSandboxOverrides(&podSpec, profile, "workspace-pvc")

	var workspacePVC, scratchEmptyDir bool
	for _, volume := range podSpec.Volumes {
		switch volume.Name {
		case "workspace":
			workspacePVC = volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == "workspace-pvc"
		case workspaceScratchVolumeName:
			scratchEmptyDir = volume.EmptyDir != nil
		}
	}
	if !workspacePVC || !scratchEmptyDir {
		t.Fatalf("workspacePVC = %v, scratchEmptyDir = %v; want both true", workspacePVC, scratchEmptyDir)
	}
}

func TestBuildCommonPodSpecDisableCommandSandbox(t *testing.T) {
	t.Setenv(sandbox.SandboxAllowUnsafeReadOnlyLocalEnv, "0")

	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Repository:            platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Model:                 "gpt-5.4",
			DisableCommandSandbox: true,
		},
	}

	spec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
	envByName := map[string]corev1.EnvVar{}
	seen := map[string]int{}
	for _, env := range spec.Containers[0].Env {
		envByName[env.Name] = env
		seen[env.Name]++
	}

	assertEnvValue(t, envValueMap(envByName), "GRATEFULAGENTS_COMMAND_SANDBOX", "disabled")
	// Read-only permission modes refuse to run outside the enforcing sandbox
	// unless this flag is set, so a complete bwrap opt-out must force it on —
	// even when the operator environment carries a conflicting value.
	assertEnvValue(t, envValueMap(envByName), sandbox.SandboxAllowUnsafeReadOnlyLocalEnv, "1")
	for _, name := range []string{"GRATEFULAGENTS_COMMAND_SANDBOX", sandbox.SandboxAllowUnsafeReadOnlyLocalEnv} {
		if seen[name] != 1 {
			t.Fatalf("env %q appears %d times, want exactly 1", name, seen[name])
		}
	}
}

func TestRuntimeProfileCommandSandboxConfigOverridesOperatorEnv(t *testing.T) {
	t.Setenv(sandbox.SandboxPathPrependEnv, "/operator/bin")

	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Model:      "gpt-5.4",
		},
	}
	podSpec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
	profile := &platformv1alpha1.RuntimeProfile{
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Sandbox: &platformv1alpha1.RuntimeProfileSandbox{
				CommandSandbox: &platformv1alpha1.RuntimeProfileCommandSandbox{
					PathPrepend:        []string{"/profile/bin", "relative", "/profile/bin", "/workspace/bin", "/tmp/bin"},
					PathAppend:         []string{"/tail/bin", "/workspace/repo/node_modules/.bin", workspaceScratchPath + "/go/bin", "/workspace/repo/bin", "/tmp/bin"},
					ExtraReadOnlyPaths: []string{"/opt/profile-toolchain", "/var/run/secrets/kubernetes.io/serviceaccount"},
					ExtraWritablePaths: []string{"/cache/go"},
					Env: map[string]string{
						"BAD-NAME":       "ignored",
						"JAVA_HOME":      "/opt/jdk",
						"OPENAI_API_KEY": "not-allowed",
						"TOOL_PATH":      "$PATH:/extra\nINJECTED=value",
					},
				},
			},
		},
	}

	applyRuntimeProfileSandboxOverrides(&podSpec, profile, "")
	envByName := map[string]corev1.EnvVar{}
	for _, env := range podSpec.Containers[0].Env {
		envByName[env.Name] = env
	}
	values := envValueMap(envByName)
	assertEnvValue(t, values, sandbox.SandboxPathPrependEnv, "/profile/bin")
	assertEnvValue(t, values, sandbox.SandboxPathAppendEnv,
		"/tail/bin:/workspace/repo/node_modules/.bin:"+workspaceScratchPath+"/go/bin")
	assertEnvValue(t, values, sandbox.SandboxExtraReadOnlyPathsEnv, "/opt/profile-toolchain")
	assertEnvValue(t, values, sandbox.SandboxExtraWritablePathsEnv, workspaceScratchPath+":/cache/go")
	assertEnvValue(t, values, sandbox.SandboxExtraEnvEnv,
		"JAVA_HOME=/opt/jdk\nTOOL_PATH=$PATH:/extra INJECTED=value\n"+
			"GOPATH="+workspaceScratchPath+"/go\nGOMODCACHE="+workspaceScratchPath+"/go/pkg/mod\n"+
			"GOCACHE="+workspaceScratchPath+"/go-build")
}

func TestRuntimeProfileCommandSandboxExtraWritablePathsPropagates(t *testing.T) {
	profile := &platformv1alpha1.RuntimeProfile{
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Sandbox: &platformv1alpha1.RuntimeProfileSandbox{
				CommandSandbox: &platformv1alpha1.RuntimeProfileCommandSandbox{
					ExtraWritablePaths: []string{"/cache/go"},
				},
			},
		},
	}

	values := envSliceValueMap(runtimeProfileCommandSandboxConfigEnvs(profile))
	assertEnvValue(t, values, sandbox.SandboxExtraWritablePathsEnv, "/cache/go")
}

func TestRuntimeProfileCommandSandboxExtraWritablePathsDeduplicates(t *testing.T) {
	profile := &platformv1alpha1.RuntimeProfile{
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Sandbox: &platformv1alpha1.RuntimeProfileSandbox{
				CommandSandbox: &platformv1alpha1.RuntimeProfileCommandSandbox{
					ExtraWritablePaths: []string{"/cache/go", " /cache/go/ ", "/cache/npm"},
				},
			},
		},
	}

	values := envSliceValueMap(runtimeProfileCommandSandboxConfigEnvs(profile))
	assertEnvValue(t, values, sandbox.SandboxExtraWritablePathsEnv, "/cache/go:/cache/npm")
}

func TestRuntimeProfileCommandSandboxExtraWritablePathsRemovesForbiddenPaths(t *testing.T) {
	profile := &platformv1alpha1.RuntimeProfile{
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Sandbox: &platformv1alpha1.RuntimeProfileSandbox{
				CommandSandbox: &platformv1alpha1.RuntimeProfileCommandSandbox{
					ExtraWritablePaths: []string{
						"/", "/etc", "/etc/ssl", "/usr", "/usr/local/bin", "/bin", "/bin/sh", "/sbin", "/sbin/init", "/lib", "/lib/x86_64-linux-gnu", "/lib64", "/lib64/ld-linux-x86-64.so.2", "/home", "/root", "/run", "/var/run", "/var/lib", "/proc", "/dev", "/tmp", "/workspace", "/workspace/repo", "/cache/go",
					},
				},
			},
		},
	}

	values := envSliceValueMap(runtimeProfileCommandSandboxConfigEnvs(profile))
	assertEnvValue(t, values, sandbox.SandboxExtraWritablePathsEnv, "/cache/go")
}

func TestRuntimeProfileCommandSandboxCoreRootsRemainAvailableToReadOnlyAndPATHConfig(t *testing.T) {
	profile := &platformv1alpha1.RuntimeProfile{
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Sandbox: &platformv1alpha1.RuntimeProfileSandbox{
				CommandSandbox: &platformv1alpha1.RuntimeProfileCommandSandbox{
					Path:               []string{"/bin", "/sbin", "/lib", "/lib64"},
					ExtraReadOnlyPaths: []string{"/bin", "/sbin", "/lib", "/lib64"},
				},
			},
		},
	}

	values := envSliceValueMap(runtimeProfileCommandSandboxConfigEnvs(profile))
	assertEnvValue(t, values, sandbox.SandboxPathEnv, "/bin:/sbin:/lib:/lib64")
	assertEnvValue(t, values, sandbox.SandboxExtraReadOnlyPathsEnv, "/bin:/sbin:/lib:/lib64")
}

func TestBuildCommonPodSpecOpenAIAPIKeyModeKeepsLegacyEnv(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Model:      "gpt-5.3-codex",
			AuthMode:   platformv1alpha1.AgentRunAuthModeAPIKey,
			Secrets:    &platformv1alpha1.AgentRunSecrets{ClaudeAPIKeySecret: "openai-key"},
		},
	}

	spec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
	envByName := map[string]corev1.EnvVar{}
	for _, env := range spec.Containers[0].Env {
		envByName[env.Name] = env
	}

	assertEnvValue(t, envValueMap(envByName), "OPENAI_AUTH_MODE", "api-key")
	if _, ok := envByName["OPENAI_API_KEY"]; !ok {
		t.Fatalf("OPENAI_API_KEY env should be present in api-key mode")
	}
	if hasSecretVolume(spec.Volumes, openAIOAuthVolumeName, "openai-oauth") {
		t.Fatalf("unexpected oauth volume in api-key mode")
	}
}

func envValueMap(envs map[string]corev1.EnvVar) map[string]string {
	out := map[string]string{}
	for name, env := range envs {
		out[name] = env.Value
	}
	return out
}

func envSliceValueMap(envs []corev1.EnvVar) map[string]string {
	out := map[string]string{}
	for _, env := range envs {
		out[env.Name] = env.Value
	}
	return out
}

func hasVolumeMount(mounts []corev1.VolumeMount, name, path string) bool {
	for _, mount := range mounts {
		if mount.Name == name && mount.MountPath == path {
			return true
		}
	}
	return false
}

func hasSecretVolume(volumes []corev1.Volume, name, secretName string) bool {
	for _, volume := range volumes {
		if volume.Name != name {
			continue
		}
		if volume.VolumeSource.Secret != nil && volume.VolumeSource.Secret.SecretName == secretName {
			return true
		}
		if volume.VolumeSource.Projected == nil {
			continue
		}
		for _, source := range volume.VolumeSource.Projected.Sources {
			if source.Secret != nil && source.Secret.Name == secretName {
				return true
			}
		}
	}
	return false
}

func secretVolumeProjectsKey(volumes []corev1.Volume, name, key string) bool {
	for _, volume := range volumes {
		if volume.Name != name {
			continue
		}
		if volume.VolumeSource.Secret != nil {
			items := volume.VolumeSource.Secret.Items
			if len(items) != 1 {
				return false
			}
			return items[0].Key == key
		}
		if volume.VolumeSource.Projected == nil {
			continue
		}
		for _, source := range volume.VolumeSource.Projected.Sources {
			if source.Secret == nil {
				continue
			}
			for _, item := range source.Secret.Items {
				if item.Key == key {
					return true
				}
			}
		}
	}
	return false
}

func hasProjectedSecretOptional(volumes []corev1.Volume, name, key string) bool {
	for _, volume := range volumes {
		if volume.Name != name || volume.VolumeSource.Projected == nil {
			continue
		}
		for _, source := range volume.VolumeSource.Projected.Sources {
			if source.Secret == nil || source.Secret.Optional == nil {
				continue
			}
			if !*source.Secret.Optional {
				continue
			}
			for _, item := range source.Secret.Items {
				if item.Key == key {
					return true
				}
			}
		}
	}
	return false
}

func assertProjectedOptionalAccountID(t *testing.T, volumes []corev1.Volume) {
	t.Helper()
	if !hasProjectedSecretOptional(volumes, openAIOAuthVolumeName, "account-id") {
		t.Fatalf("expected projected optional account-id source in volume %q", openAIOAuthVolumeName)
	}
}

func assertEnvValue(t *testing.T, values map[string]string, key, want string) {
	t.Helper()
	if got := values[key]; got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

func TestProviderEnvVarName(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"anthropic", "ANTHROPIC_API_KEY"},
		{"openai", "OPENAI_API_KEY"},
		{"openrouter", "OPENROUTER_API_KEY"},
		{"gemini", "GEMINI_API_KEY"},
		{"groq", "GROQ_API_KEY"},
		{"xai", "XAI_API_KEY"},
		{"copilot", "COPILOT_API_KEY"},
		{"ANTHROPIC", "ANTHROPIC_API_KEY"},
		{"custom", "CUSTOM_API_KEY"},
	}
	for _, tt := range tests {
		if got := providerEnvVarName(tt.provider); got != tt.want {
			t.Errorf("providerEnvVarName(%q) = %q, want %q", tt.provider, got, tt.want)
		}
	}
}

func TestProviderAPIKeyEnvsMultipleProviders(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Model: "anthropic/claude-sonnet-4-6",
			Secrets: &platformv1alpha1.AgentRunSecrets{
				ProviderKeys: []platformv1alpha1.ProviderKeyRef{
					{Provider: "anthropic", SecretName: "anthropic-secret"},
					{Provider: "openai", SecretName: "openai-secret"},
					{Provider: "openrouter", SecretName: "or-secret", SecretKey: "token"},
				},
			},
		},
	}
	envs := providerAPIKeyEnvs(run)
	envByName := map[string]corev1.EnvVar{}
	for _, e := range envs {
		envByName[e.Name] = e
	}
	if len(envs) != 3 {
		t.Fatalf("expected 3 env vars, got %d", len(envs))
	}
	if envByName["ANTHROPIC_API_KEY"].ValueFrom.SecretKeyRef.Name != "anthropic-secret" {
		t.Fatalf("ANTHROPIC_API_KEY should reference anthropic-secret")
	}
	if envByName["OPENAI_API_KEY"].ValueFrom.SecretKeyRef.Key != "api-key" {
		t.Fatalf("OPENAI_API_KEY should default to api-key key")
	}
	if envByName["OPENROUTER_API_KEY"].ValueFrom.SecretKeyRef.Key != "token" {
		t.Fatalf("OPENROUTER_API_KEY should use custom secretKey 'token'")
	}
}

func TestProviderAPIKeyEnvsLegacyFallback(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Model: "anthropic/claude-sonnet-4-6",
			Secrets: &platformv1alpha1.AgentRunSecrets{
				ClaudeAPIKeySecret: "legacy-key",
			},
		},
	}
	envs := providerAPIKeyEnvs(run)
	if len(envs) != 1 {
		t.Fatalf("expected 1 env var from legacy fallback, got %d", len(envs))
	}
	if envs[0].Name != "ANTHROPIC_API_KEY" {
		t.Fatalf("expected ANTHROPIC_API_KEY, got %s", envs[0].Name)
	}
}

func TestProviderAPIKeyEnvsLegacySkippedWhenCoveredByProviderKeys(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Model: "anthropic/claude-sonnet-4-6",
			Secrets: &platformv1alpha1.AgentRunSecrets{
				ClaudeAPIKeySecret: "legacy-key",
				ProviderKeys: []platformv1alpha1.ProviderKeyRef{
					{Provider: "anthropic", SecretName: "new-anthropic-key"},
				},
			},
		},
	}
	envs := providerAPIKeyEnvs(run)
	if len(envs) != 1 {
		t.Fatalf("expected 1 env var (legacy should be skipped), got %d", len(envs))
	}
	if envs[0].ValueFrom.SecretKeyRef.Name != "new-anthropic-key" {
		t.Fatalf("expected new-anthropic-key, got %s", envs[0].ValueFrom.SecretKeyRef.Name)
	}
}

func TestProviderAPIKeyEnvsDeduplicate(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Model: "openai/gpt-5.4",
			Secrets: &platformv1alpha1.AgentRunSecrets{
				ProviderKeys: []platformv1alpha1.ProviderKeyRef{
					{Provider: "openai", SecretName: "key-1"},
					{Provider: "openai", SecretName: "key-2"},
				},
			},
		},
	}
	envs := providerAPIKeyEnvs(run)
	if len(envs) != 1 {
		t.Fatalf("expected 1 env var (duplicates skipped), got %d", len(envs))
	}
	if envs[0].ValueFrom.SecretKeyRef.Name != "key-1" {
		t.Fatalf("expected first key to win, got %s", envs[0].ValueFrom.SecretKeyRef.Name)
	}
}

func TestSlackTokensEnvs(t *testing.T) {
	if envs := slackTokensEnvs(&platformv1alpha1.AgentRun{}); envs != nil {
		t.Fatalf("expected nil envs without secrets, got %v", envs)
	}
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			Secrets: &platformv1alpha1.AgentRunSecrets{SlackTokensSecret: "alice-slack"},
		},
	}
	envs := slackTokensEnvs(run)
	if len(envs) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(envs))
	}
	byName := map[string]corev1.EnvVar{}
	for _, e := range envs {
		byName[e.Name] = e
	}
	bot := byName["SLACK_BOT_TOKEN"]
	if bot.ValueFrom == nil || bot.ValueFrom.SecretKeyRef.Name != "alice-slack" || bot.ValueFrom.SecretKeyRef.Key != "bot-token" {
		t.Fatalf("SLACK_BOT_TOKEN ref = %+v, want alice-slack/bot-token", bot.ValueFrom)
	}
	user := byName["SLACK_USER_TOKEN"]
	if user.ValueFrom == nil || user.ValueFrom.SecretKeyRef.Key != "user-token" {
		t.Fatalf("SLACK_USER_TOKEN ref = %+v, want user-token key", user.ValueFrom)
	}
	if user.ValueFrom.SecretKeyRef.Optional == nil || !*user.ValueFrom.SecretKeyRef.Optional {
		t.Fatal("slack token env refs must be optional so a missing user token doesn't block the pod")
	}
}

func TestResolveMCPServerSecretEnvs(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	required := false
	srv := &platformv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana", Namespace: "ns"},
		Spec: platformv1alpha1.MCPServerSpec{
			MCPServerConfig: &platformv1alpha1.MCPServerConfig{
				Command: "uvx",
				SecretEnv: []platformv1alpha1.MCPServerSecretEnv{
					{Name: "GRAFANA_URL", SecretName: "usercred-grafana", SecretKey: "url"},
					{Name: "GRAFANA_SERVICE_ACCOUNT_TOKEN", SecretName: "usercred-grafana", SecretKey: "token", Optional: &required},
					{Name: "GRAFANA_URL", SecretName: "other", SecretKey: "dup"}, // duplicate name skipped
				},
			},
		},
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: platformv1alpha1.AgentRunSpec{
			MCPServerRefs: []platformv1alpha1.NamedRef{{Name: "grafana"}, {Name: "missing-server"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(srv).Build()

	envs := resolveMCPServerSecretEnvs(context.Background(), c, run)
	if len(envs) != 2 {
		t.Fatalf("expected 2 envs (dup + missing server skipped), got %d: %+v", len(envs), envs)
	}
	url := envs[0]
	if url.Name != "GRAFANA_URL" || url.ValueFrom.SecretKeyRef.Name != "usercred-grafana" || url.ValueFrom.SecretKeyRef.Key != "url" {
		t.Fatalf("url env = %+v", url)
	}
	if url.ValueFrom.SecretKeyRef.Optional == nil || !*url.ValueFrom.SecretKeyRef.Optional {
		t.Fatal("secretEnv must default to optional")
	}
	token := envs[1]
	if token.ValueFrom.SecretKeyRef.Optional == nil || *token.ValueFrom.SecretKeyRef.Optional {
		t.Fatal("explicit optional=false must be honored")
	}

	if got := resolveMCPServerSecretEnvs(context.Background(), c, &platformv1alpha1.AgentRun{}); got != nil {
		t.Fatalf("expected nil for run without refs, got %v", got)
	}
}

func TestResolveMCPServerSecretEnvsViaSkillRequires(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	srv := &platformv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana", Namespace: "ns"},
		Spec: platformv1alpha1.MCPServerSpec{
			MCPServerConfig: &platformv1alpha1.MCPServerConfig{
				Command: "uvx",
				SecretEnv: []platformv1alpha1.MCPServerSecretEnv{
					{Name: "GRAFANA_URL", SecretName: "usercred-grafana", SecretKey: "url"},
				},
			},
		},
	}
	skill := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana-runbook", Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Source:   platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "use grafana well"}},
			Requires: &platformv1alpha1.SkillRequires{MCPServers: []platformv1alpha1.NamedRef{{Name: "grafana"}}},
		},
	}
	// The run references only the skill; the server comes along via requires.
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: platformv1alpha1.AgentRunSpec{
			SkillRefs: []platformv1alpha1.NamedRef{{Name: "grafana-runbook"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(srv, skill).Build()

	envs := resolveMCPServerSecretEnvs(context.Background(), c, run)
	if len(envs) != 1 || envs[0].Name != "GRAFANA_URL" {
		t.Fatalf("expected skill-required server secretEnv to be injected, got %+v", envs)
	}
}

func TestBuildCommonPodSpecGitIdentityEnv(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				platformv1alpha1.GitAuthorNameAnnotation:  "Alice Doe",
				platformv1alpha1.GitAuthorEmailAnnotation: "alice@example.com",
			},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			Model:      "gpt-5.3-codex",
		},
	}

	spec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
	envByName := map[string]corev1.EnvVar{}
	for _, env := range spec.Containers[0].Env {
		envByName[env.Name] = env
	}
	values := envValueMap(envByName)
	assertEnvValue(t, values, "GIT_AUTHOR_NAME", "Alice Doe")
	assertEnvValue(t, values, "GIT_AUTHOR_EMAIL", "alice@example.com")
	assertEnvValue(t, values, "GIT_COMMITTER_NAME", "Alice Doe")
	assertEnvValue(t, values, "GIT_COMMITTER_EMAIL", "alice@example.com")
}

func TestBuildCommonPodSpecGitIdentityEnvOmittedWhenUnset(t *testing.T) {
	cases := map[string]map[string]string{
		"no annotations": nil,
		"name only":      {platformv1alpha1.GitAuthorNameAnnotation: "Alice"},
		"email only":     {platformv1alpha1.GitAuthorEmailAnnotation: "alice@example.com"},
	}
	for name, annotations := range cases {
		run := &platformv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Annotations: annotations},
			Spec: platformv1alpha1.AgentRunSpec{
				Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
				Model:      "gpt-5.3-codex",
			},
		}
		spec := buildCommonPodSpec(run, "sa", []string{"agent", "run"}, nil, nil, nil)
		for _, env := range spec.Containers[0].Env {
			switch env.Name {
			case "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL":
				t.Fatalf("%s: unexpected env %s=%q; identity env requires both annotations", name, env.Name, env.Value)
			}
		}
	}
}
