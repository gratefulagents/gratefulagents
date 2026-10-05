package main

import (
	"context"
	"log"
	"os"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcpattach"
	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
)

// buildMCPConfig merges MCP configs from two sources into one Config:
//  1. Repo-level .mcp.json (user opt-in per repo)
//  2. MCPServer CRDs attached to the AgentRun — referenced explicitly via
//     spec.mcpServerRefs or auto-attached by a referenced Skill's
//     requires.mcpServers (cluster-managed, no repo config needed)
//
// CRD entries take precedence over .mcp.json entries with the same name.
//
// The second return value is the set of configured names sourced from MCPServer
// CRDs. The third is the subset explicitly opted into network access.
func buildMCPConfig(ctx context.Context, c client.Client, namespace, workDir string, run *platformv1alpha1.AgentRun) (sdkmcp.Config, map[string]struct{}, map[string]struct{}) {
	cfg := sdkmcp.Config{MCPServers: make(map[string]sdkmcp.ServerConfig)}
	clusterManaged := make(map[string]struct{})
	networkAllowed := make(map[string]struct{})

	// Source 1: repo .mcp.json (optional).
	cfgPath := sdkmcp.ConfigPathForWorkDir(workDir)
	repoCfg, exists, err := sdkmcp.LoadConfig(cfgPath)
	if err != nil {
		log.Printf("WARN: loading repo .mcp.json: %v", err)
	} else if exists {
		for name, srv := range repoCfg.MCPServers {
			cfg.MCPServers[name] = srv
		}
		log.Printf("Loaded %d MCP server(s) from repo .mcp.json", len(repoCfg.MCPServers))
	}

	// Source 2: MCPServer CRDs (cluster-managed), including servers required
	// by attached skills.
	if run == nil {
		return cfg, clusterManaged, networkAllowed
	}
	// A transient API failure while reading skills must not silently drop
	// their required servers for the pod's whole lifetime: retry briefly, then
	// fall back to the run's explicit refs with a visible warning.
	var refs []platformv1alpha1.NamedRef
	if err := retryTransient(ctx, "resolving MCP servers required by skills", 5, func(ctx context.Context) error {
		resolved, err := mcpattach.EffectiveMCPServerRefs(ctx, c, run)
		if err != nil {
			return err
		}
		refs = resolved
		return nil
	}); err != nil {
		log.Printf("WARN: resolving MCP servers required by skills: %v — attaching explicit refs only", err)
		refs, _ = mcpattach.EffectiveMCPServerRefs(ctx, nil, run)
	}
	if len(refs) == 0 {
		return cfg, clusterManaged, networkAllowed
	}
	crdCount := 0
	for _, ref := range refs {
		srv := &platformv1alpha1.MCPServer{}
		key := types.NamespacedName{Name: ref.Name, Namespace: namespace}
		if err := c.Get(ctx, key, srv); err != nil {
			log.Printf("WARN: failed to fetch MCPServer %s: %v — skipping", key, err)
			continue
		}
		if srv.Spec.MCPServerConfig == nil || srv.Spec.MCPServerConfig.Command == "" {
			log.Printf("MCPServer %s has no server config — skipping", srv.Name)
			continue
		}
		cfg.MCPServers[srv.Name] = crdServerConfig(srv)
		if srv.Spec.MCPServerConfig.AllowNetwork {
			networkAllowed[srv.Name] = struct{}{}
		}
		clusterManaged[srv.Name] = struct{}{}
		crdCount++
	}
	if crdCount > 0 {
		log.Printf("Resolved %d MCPServer CRD(s) into MCP config", crdCount)
	}
	return cfg, clusterManaged, networkAllowed
}

// crdServerConfig converts an MCPServer CRD into the SDK server config,
// bridging secretEnv credentials from the pod environment.
//
// The run pod receives each spec.mcpServerConfig.secretEnv entry as a
// server-scoped secretKeyRef env var, but the SDK deliberately never passes the agent
// process environment to MCP subprocesses (children get a minimal safe env
// plus the per-server env map). So the value must be copied into the server's
// env map here, in-memory only — it is never written to disk, and the SDK
// logs env *names* only. The name is also appended to allowEnv so the SDK's
// credential filter passes it through without users having to remember the
// secretEnv/allowEnv pairing.
//
// Known limitation: env values pass through one ${VAR} expansion against the
// SDK's safe base env, so secret values containing a literal '$' can be
// altered before reaching the server.
func crdServerConfig(srv *platformv1alpha1.MCPServer) sdkmcp.ServerConfig {
	mc := srv.Spec.MCPServerConfig
	env := make(map[string]string, len(mc.Env)+len(mc.SecretEnv))
	for k, v := range mc.Env {
		env[k] = v
	}
	allowEnv := append([]string(nil), mc.AllowEnv...)
	allowed := make(map[string]bool, len(allowEnv))
	for _, name := range allowEnv {
		allowed[name] = true
	}
	var missing []string
	for _, se := range mc.SecretEnv {
		name := strings.TrimSpace(se.Name)
		if name == "" {
			continue
		}
		podName := mcpattach.SecretEnvPodName(srv.Name, name)
		v, ok := os.LookupEnv(podName)
		// Keep compatibility with pods created by older controllers. This path
		// cannot isolate duplicate names, but avoids breaking single-server runs
		// during a rolling controller/worker deployment.
		if !ok || v == "" {
			v, ok = os.LookupEnv(name)
		}
		if ok && v != "" {
			env[name] = v
		} else if _, inline := env[name]; !inline {
			missing = append(missing, name)
		}
		if !allowed[name] {
			allowed[name] = true
			allowEnv = append(allowEnv, name)
		}
	}
	if len(missing) > 0 {
		log.Printf("WARN: MCPServer %s: secretEnv var(s) not present in pod env: %s — the server may fail to authenticate",
			srv.Name, strings.Join(missing, ", "))
	}
	if len(env) == 0 {
		env = nil
	}
	return sdkmcp.ServerConfig{
		Type:              mc.Type,
		Command:           mc.Command,
		Args:              mc.Args,
		Env:               env,
		AllowEnv:          allowEnv,
		TrustReadOnlyHint: mc.TrustReadOnlyHint,
	}
}

// mcpPromptContext builds the per-turn prompt block describing the connected
// MCP servers so the agent knows the tools exist and how they are named
// (parity with the SDK-native runner's MCP prompt section). Server names can
// originate from the agent-writable repo .mcp.json, so they are sanitized
// before being placed in the prompt.
func mcpPromptContext(serverNames []string) string {
	if len(serverNames) == 0 {
		return ""
	}
	cleaned := make([]string, 0, len(serverNames))
	for _, name := range serverNames {
		if s := sanitizeMCPNameForPrompt(name); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	return "# MCP servers\n\nConnected MCP servers: " + strings.Join(cleaned, ", ") +
		"\nTheir tools are registered as mcp__<server>__<tool>."
}

var mcpPromptNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitizeMCPNameForPrompt(name string) string {
	s := mcpPromptNameSanitizer.ReplaceAllString(strings.TrimSpace(name), "_")
	const maxLen = 64
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return strings.Trim(s, "_")
}
