package platform

// Dashboard and manager-side pod inspection paths use the controller-manager
// service account, so these permissions must be generated into the manager role.
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=delete
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=guardrailpolicies;mcppolicies;roleinstructions;runtimeprofiles,verbs=get;list;watch
// The dashboard provisions namespaced RuntimeProfiles, MCPPolicies, and
// GuardrailPolicies for users and exposes admin-managed ModeTemplates and
// RoleInstructions. It needs full mutation verbs for create, edit, delete, and
// rollback paths across those resources.
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=guardrailpolicies;mcppolicies;modetemplates;roleinstructions;runtimeprofiles,verbs=create;update;patch;delete
// The reconciler syncs an infra credentials Secret (S3 + database) into each
// namespace that hosts worker pods so credentials are never inlined in pod specs.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update
// The dashboard provisions a personal namespace per user (where their projects and
// saved credential Secrets live), so it needs to create and read namespaces.
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create
// Kubernetes-admin dogfooding runs bind their worker service account to the
// built-in cluster-admin ClusterRole. The bind verb is constrained to that
// single ClusterRole.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind,resourceNames=cluster-admin
