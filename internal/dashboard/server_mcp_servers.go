package dashboard

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"connectrpc.com/connect"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
)

// mcpServerNameRe constrains server names to DNS-safe lowercase so the CR
// name is always valid and matches mcpServerRefs conventions.
var mcpServerNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,51}[a-z0-9])?$`)

// ListMCPServers lists the MCP server configs (MCPServer CRDs) available in
// the caller's namespace, with the full config echoed so the settings form can
// edit servers in place.
func (s *Server) ListMCPServers(ctx context.Context, _ *platform.ListMCPServersRequest) (*platform.ListMCPServersResponse, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	var list platformv1alpha1.MCPServerList
	if err := s.k8sClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, mapK8sError("list MCP servers", err)
	}
	resp := &platform.ListMCPServersResponse{Namespace: namespace}
	for i := range list.Items {
		resp.Servers = append(resp.Servers, mcpServerInfo(&list.Items[i]))
	}
	sort.Slice(resp.Servers, func(i, j int) bool { return resp.Servers[i].Name < resp.Servers[j].Name })
	return resp, nil
}

// UpsertMCPServer creates or updates an MCPServer CR in the caller's
// namespace from structured form fields. The whole spec is assigned on every
// save (the form owns it).
func (s *Server) UpsertMCPServer(ctx context.Context, req *platform.UpsertMCPServerRequest) (*platform.MCPServerInfo, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(req.GetName()))
	if !mcpServerNameRe.MatchString(name) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("server name %q must be lowercase letters, digits, and hyphens", req.GetName()))
	}
	command := strings.TrimSpace(req.GetCommand())
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("command is required"))
	}
	secretEnv, err := mcpServerSecretEnvSpecs(req.GetSecretEnv())
	if err != nil {
		return nil, err
	}
	env := trimmedMap(req.GetEnv())
	if err := validateMCPServerPlainEnv(env); err != nil {
		return nil, err
	}

	spec := platformv1alpha1.MCPServerSpec{
		Version:     strings.TrimSpace(req.GetVersion()),
		Description: strings.TrimSpace(req.GetDescription()),
		MCPServerConfig: &platformv1alpha1.MCPServerConfig{
			Type:              "stdio",
			Command:           command,
			Args:              trimmedList(req.GetArgs()),
			Env:               env,
			AllowEnv:          trimmedList(req.GetAllowEnv()),
			SecretEnv:         secretEnv,
			TrustReadOnlyHint: req.GetTrustReadOnlyHint(),
			AllowNetwork:      req.GetAllowNetwork(),
		},
	}

	srv := &platformv1alpha1.MCPServer{}
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := s.k8sClient.Get(ctx, key, srv); err != nil {
		if !k8serrors.IsNotFound(err) {
			return nil, mapK8sError("read MCP server", err)
		}
		srv = &platformv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       spec,
		}
		if err := s.k8sClient.Create(ctx, srv); err != nil {
			return nil, mapK8sError("create MCP server", err)
		}
		return mcpServerInfo(srv), nil
	}
	srv.Spec = spec
	if err := s.k8sClient.Update(ctx, srv); err != nil {
		return nil, mapK8sError("update MCP server", err)
	}
	return mcpServerInfo(srv), nil
}

// DeleteMCPServer removes an MCPServer from the caller's namespace. Deleting
// a server other agents or skills still reference is safe: refs to missing
// servers are skipped at run time.
func (s *Server) DeleteMCPServer(ctx context.Context, req *platform.DeleteMCPServerRequest) error {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return err
	}
	name := strings.ToLower(strings.TrimSpace(req.GetName()))
	if name == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server name is required"))
	}
	srv := &platformv1alpha1.MCPServer{}
	srv.Name = name
	srv.Namespace = namespace
	if err := s.k8sClient.Delete(ctx, srv); err != nil && !k8serrors.IsNotFound(err) {
		return mapK8sError("delete MCP server", err)
	}
	return nil
}

// mcpServerInfo converts a CR into its wire form (no secret values exist on
// the CR; secretEnv carries references only).
func mcpServerInfo(srv *platformv1alpha1.MCPServer) *platform.MCPServerInfo {
	info := &platform.MCPServerInfo{
		Name:        srv.Name,
		Version:     srv.Spec.Version,
		Description: strings.TrimSpace(srv.Spec.Description),
	}
	if mc := srv.Spec.MCPServerConfig; mc != nil {
		info.Command = mc.Command
		info.Args = mc.Args
		info.Env = mc.Env
		info.AllowEnv = mc.AllowEnv
		info.TrustReadOnlyHint = mc.TrustReadOnlyHint
		info.AllowNetwork = mc.AllowNetwork
		for _, se := range mc.SecretEnv {
			required := se.Optional != nil && !*se.Optional
			info.SecretEnv = append(info.SecretEnv, &platform.MCPServerSecretEnv{
				Name:       se.Name,
				SecretName: se.SecretName,
				SecretKey:  se.SecretKey,
				Required:   required,
			})
		}
	}
	return info
}

// mcpServerSecretEnvSpecs validates and converts wire secret-env entries.
// Dashboard-created servers may only reference the caller's saved integration
// credentials (usercred-* Secrets): letting the RPC name arbitrary Secrets
// would turn any MCPServer into a vehicle for mounting any Secret in the
// namespace into a run pod. Cluster admins creating MCPServer CRs directly
// (kubectl/GitOps) are not restricted.
func mcpServerSecretEnvSpecs(entries []*platform.MCPServerSecretEnv) ([]platformv1alpha1.MCPServerSecretEnv, error) {
	var out []platformv1alpha1.MCPServerSecretEnv
	for _, e := range entries {
		name := strings.TrimSpace(e.GetName())
		secretName := strings.TrimSpace(e.GetSecretName())
		secretKey := strings.TrimSpace(e.GetSecretKey())
		if name == "" && secretName == "" && secretKey == "" {
			continue // empty form row
		}
		if name == "" || secretName == "" || secretKey == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("secret env entries need env name, secret name, and secret key"))
		}
		if !strings.HasPrefix(secretName, userCredentialSecretPrefix) {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("secret env %q: secret %q is not a saved integration credential (%s*); save the credential under Settings → Integrations first",
					name, secretName, userCredentialSecretPrefix))
		}
		optional := !e.GetRequired()
		out = append(out, platformv1alpha1.MCPServerSecretEnv{
			Name:       name,
			SecretName: secretName,
			SecretKey:  secretKey,
			Optional:   &optional,
		})
	}
	return out, nil
}

// trimmedList drops empty entries and trims the rest.
func trimmedList(in []string) []string {
	var out []string
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// trimmedMap drops empty keys/values and trims the rest.
func trimmedMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k != "" && v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateMCPServerPlainEnv rejects credential-looking keys in the plaintext
// env map: those values would be stored verbatim in the CRD (etcd) and are
// exactly what secretEnv exists for.
func validateMCPServerPlainEnv(env map[string]string) error {
	for key := range env {
		if sdkmcp.IsCredentialEnvName(key) {
			return connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("env %q looks like a credential and would be stored in plaintext; use a secret credential (secretEnv) instead", key))
		}
	}
	return nil
}
