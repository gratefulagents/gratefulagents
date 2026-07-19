package mcppolicy

import (
	"encoding/json"
	"sort"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

const (
	PendingRequestAnnotation = "platform.gratefulagents.dev/mcp-break-glass-request"
	GrantsAnnotation         = "platform.gratefulagents.dev/mcp-break-glass-grants"
	DecisionsAnnotation      = "platform.gratefulagents.dev/mcp-break-glass-decisions"
)

type BreakGlassRequest struct {
	ID          string `json:"id,omitempty"`
	Server      string `json:"server"`
	Tool        string `json:"tool,omitempty"`
	Reason      string `json:"reason,omitempty"`
	RequestedAt string `json:"requestedAt,omitempty"`
	RequestedBy string `json:"requestedBy,omitempty"`
}

type BreakGlassGrant struct {
	RequestID   string `json:"requestId,omitempty"`
	Server      string `json:"server"`
	Tool        string `json:"tool,omitempty"`
	Reason      string `json:"reason,omitempty"`
	RequestedAt string `json:"requestedAt,omitempty"`
	RequestedBy string `json:"requestedBy,omitempty"`
	ApprovedAt  string `json:"approvedAt,omitempty"`
	ApprovedBy  string `json:"approvedBy,omitempty"`
}

// BreakGlassDecision is a durable request-bound audit tombstone used to make
// both approval and rejection replay-safe.
type BreakGlassDecision struct {
	RequestID string `json:"requestId"`
	Decision  string `json:"decision"`
	DecidedAt string `json:"decidedAt,omitempty"`
	DecidedBy string `json:"decidedBy,omitempty"`
}

type Evaluator struct {
	defaultAction string
	breakGlass    platformv1alpha1.MCPBreakGlass
	servers       map[string]allowedServer
	grants        []BreakGlassGrant
}

type allowedServer struct {
	name  string
	tools map[string]struct{}
}

func NewEvaluator(run *platformv1alpha1.AgentRun, policy *platformv1alpha1.MCPPolicy) Evaluator {
	if policy == nil {
		policy = fallbackPolicyFromRun(run)
	}

	evaluator := Evaluator{
		defaultAction: normalizeDefaultAction(policy),
		servers:       make(map[string]allowedServer),
	}
	if policy != nil && policy.Spec.BreakGlass != nil {
		evaluator.breakGlass = *policy.Spec.BreakGlass
	}
	if policy != nil {
		for _, server := range policy.Spec.AllowedServers {
			name := normalizeCapabilityName(server.Name)
			if name == "" {
				continue
			}
			entry := allowedServer{
				name:  strings.TrimSpace(server.Name),
				tools: make(map[string]struct{}, len(server.Tools)),
			}
			for _, tool := range server.Tools {
				if normalizedTool := normalizeCapabilityName(tool); normalizedTool != "" {
					entry.tools[normalizedTool] = struct{}{}
				}
			}
			evaluator.servers[name] = entry
		}
	}
	grants, err := GrantedGrants(run)
	if err == nil {
		evaluator.grants = grants
	}
	return evaluator
}

func (e Evaluator) DefaultAction() string {
	if e.defaultAction == "" {
		return "deny"
	}
	return e.defaultAction
}

func (e Evaluator) BreakGlass() platformv1alpha1.MCPBreakGlass {
	return e.breakGlass
}

func (e Evaluator) BreakGlassEnabled() bool {
	return e.breakGlass.Enabled
}

func (e Evaluator) AllowsServer(server string) bool {
	serverKey := normalizeCapabilityName(server)
	if serverKey == "" {
		return false
	}
	if e.grantAllows(serverKey, "") {
		return true
	}
	if _, ok := e.servers[serverKey]; ok {
		return true
	}
	return e.DefaultAction() == "allow"
}

func (e Evaluator) AllowsTool(server, tool string) bool {
	serverKey := normalizeCapabilityName(server)
	if serverKey == "" {
		return false
	}
	toolKey := normalizeCapabilityName(tool)
	if e.grantAllows(serverKey, toolKey) {
		return true
	}
	if entry, ok := e.servers[serverKey]; ok {
		if len(entry.tools) == 0 || toolKey == "" {
			return true
		}
		_, ok := entry.tools[toolKey]
		return ok
	}
	return e.DefaultAction() == "allow"
}

func ExplicitAllowedServers(policy *platformv1alpha1.MCPPolicy) []string {
	if policy == nil || len(policy.Spec.AllowedServers) == 0 {
		return nil
	}
	out := make([]string, 0, len(policy.Spec.AllowedServers))
	for _, server := range policy.Spec.AllowedServers {
		if trimmed := strings.TrimSpace(server.Name); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func SameBreakGlassRequest(a, b *BreakGlassRequest) bool {
	return a != nil && b != nil && a.ID == b.ID && a.Server == b.Server && a.Tool == b.Tool && a.Reason == b.Reason && a.RequestedAt == b.RequestedAt && a.RequestedBy == b.RequestedBy
}

func PendingRequest(run *platformv1alpha1.AgentRun) (*BreakGlassRequest, error) {
	if run == nil || len(run.Annotations) == 0 {
		return nil, nil
	}
	raw := strings.TrimSpace(run.Annotations[PendingRequestAnnotation])
	if raw == "" {
		return nil, nil
	}
	var request BreakGlassRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return nil, err
	}
	if normalizeCapabilityName(request.Server) == "" {
		return nil, nil
	}
	request.ID = strings.TrimSpace(request.ID)
	request.Server = strings.TrimSpace(request.Server)
	request.Tool = strings.TrimSpace(request.Tool)
	request.Reason = strings.TrimSpace(request.Reason)
	request.RequestedAt = strings.TrimSpace(request.RequestedAt)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	return &request, nil
}

func GrantedGrants(run *platformv1alpha1.AgentRun) ([]BreakGlassGrant, error) {
	if run == nil || len(run.Annotations) == 0 {
		return nil, nil
	}
	raw := strings.TrimSpace(run.Annotations[GrantsAnnotation])
	if raw == "" {
		return nil, nil
	}
	var grants []BreakGlassGrant
	if err := json.Unmarshal([]byte(raw), &grants); err != nil {
		return nil, err
	}
	out := make([]BreakGlassGrant, 0, len(grants))
	for _, grant := range grants {
		if normalizeCapabilityName(grant.Server) == "" {
			continue
		}
		grant.RequestID = strings.TrimSpace(grant.RequestID)
		grant.Server = strings.TrimSpace(grant.Server)
		grant.Tool = strings.TrimSpace(grant.Tool)
		grant.Reason = strings.TrimSpace(grant.Reason)
		grant.RequestedAt = strings.TrimSpace(grant.RequestedAt)
		grant.RequestedBy = strings.TrimSpace(grant.RequestedBy)
		grant.ApprovedAt = strings.TrimSpace(grant.ApprovedAt)
		grant.ApprovedBy = strings.TrimSpace(grant.ApprovedBy)
		out = append(out, grant)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server == out[j].Server {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Server < out[j].Server
	})
	return out, nil
}

func BreakGlassDecisions(run *platformv1alpha1.AgentRun) ([]BreakGlassDecision, error) {
	if run == nil || len(run.Annotations) == 0 {
		return nil, nil
	}
	raw := strings.TrimSpace(run.Annotations[DecisionsAnnotation])
	if raw == "" {
		return nil, nil
	}
	var decisions []BreakGlassDecision
	if err := json.Unmarshal([]byte(raw), &decisions); err != nil {
		return nil, err
	}
	out := make([]BreakGlassDecision, 0, len(decisions))
	for _, decision := range decisions {
		decision.RequestID = strings.TrimSpace(decision.RequestID)
		decision.Decision = strings.ToLower(strings.TrimSpace(decision.Decision))
		decision.DecidedAt = strings.TrimSpace(decision.DecidedAt)
		decision.DecidedBy = strings.TrimSpace(decision.DecidedBy)
		if decision.RequestID == "" || (decision.Decision != "approved" && decision.Decision != "denied") {
			continue
		}
		out = append(out, decision)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestID < out[j].RequestID })
	return out, nil
}

func SetBreakGlassDecisions(annotations map[string]string, decisions []BreakGlassDecision) error {
	if annotations == nil {
		return nil
	}
	if len(decisions) == 0 {
		delete(annotations, DecisionsAnnotation)
		return nil
	}
	payload, err := json.Marshal(decisions)
	if err != nil {
		return err
	}
	annotations[DecisionsAnnotation] = string(payload)
	return nil
}

func UpsertBreakGlassDecision(decisions []BreakGlassDecision, decision BreakGlassDecision) []BreakGlassDecision {
	requestID := strings.TrimSpace(decision.RequestID)
	if requestID == "" {
		return decisions
	}
	out := make([]BreakGlassDecision, 0, len(decisions)+1)
	for _, existing := range decisions {
		if strings.TrimSpace(existing.RequestID) != requestID {
			out = append(out, existing)
		}
	}
	decision.RequestID = requestID
	decision.Decision = strings.ToLower(strings.TrimSpace(decision.Decision))
	out = append(out, decision)
	sort.Slice(out, func(i, j int) bool { return out[i].RequestID < out[j].RequestID })
	return out
}

func FindBreakGlassDecision(decisions []BreakGlassDecision, requestID string) *BreakGlassDecision {
	requestID = strings.TrimSpace(requestID)
	for i := range decisions {
		if strings.TrimSpace(decisions[i].RequestID) == requestID && requestID != "" {
			return &decisions[i]
		}
	}
	return nil
}

func SetPendingRequest(annotations map[string]string, request BreakGlassRequest) error {
	if annotations == nil {
		return nil
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	annotations[PendingRequestAnnotation] = string(payload)
	return nil
}

func ClearPendingRequest(annotations map[string]string) {
	if annotations == nil {
		return
	}
	delete(annotations, PendingRequestAnnotation)
}

func SetGrantedGrants(annotations map[string]string, grants []BreakGlassGrant) error {
	if annotations == nil {
		return nil
	}
	if len(grants) == 0 {
		delete(annotations, GrantsAnnotation)
		return nil
	}
	payload, err := json.Marshal(grants)
	if err != nil {
		return err
	}
	annotations[GrantsAnnotation] = string(payload)
	return nil
}

func UpsertGrant(grants []BreakGlassGrant, grant BreakGlassGrant) []BreakGlassGrant {
	serverKey := normalizeCapabilityName(grant.Server)
	toolKey := normalizeCapabilityName(grant.Tool)
	if serverKey == "" {
		return grants
	}
	out := make([]BreakGlassGrant, 0, len(grants)+1)
	replaced := false
	for _, existing := range grants {
		if normalizeCapabilityName(existing.Server) == serverKey && normalizeCapabilityName(existing.Tool) == toolKey {
			if !replaced {
				out = append(out, grant)
				replaced = true
			}
			continue
		}
		out = append(out, existing)
	}
	if !replaced {
		out = append(out, grant)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server == out[j].Server {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Server < out[j].Server
	})
	return out
}

func RemoveBreakGlassGrantByRequestID(grants []BreakGlassGrant, requestID string) []BreakGlassGrant {
	requestID = strings.TrimSpace(requestID)
	out := make([]BreakGlassGrant, 0, len(grants))
	for _, grant := range grants {
		if requestID != "" && strings.TrimSpace(grant.RequestID) == requestID {
			continue
		}
		out = append(out, grant)
	}
	return out
}

func normalizeDefaultAction(policy *platformv1alpha1.MCPPolicy) string {
	if policy == nil {
		return "deny"
	}
	if strings.EqualFold(strings.TrimSpace(string(policy.Spec.DefaultAction)), "allow") {
		return "allow"
	}
	return "deny"
}

func normalizeCapabilityName(name string) string {
	return strings.TrimSpace(name)
}

func (e Evaluator) grantAllows(server, tool string) bool {
	for _, grant := range e.grants {
		if normalizeCapabilityName(grant.Server) != server {
			continue
		}
		grantTool := normalizeCapabilityName(grant.Tool)
		if grantTool == "" || grantTool == tool {
			return true
		}
	}
	return false
}

func fallbackPolicyFromRun(run *platformv1alpha1.AgentRun) *platformv1alpha1.MCPPolicy {
	if run == nil || run.Status.Policy == nil || len(run.Status.Policy.ResolvedMCPServers) == 0 {
		return nil
	}
	policy := &platformv1alpha1.MCPPolicy{
		Spec: platformv1alpha1.MCPPolicySpec{
			DefaultAction:  platformv1alpha1.MCPDefaultActionDeny,
			AllowedServers: make([]platformv1alpha1.MCPAllowedServer, 0, len(run.Status.Policy.ResolvedMCPServers)),
		},
	}
	for _, server := range run.Status.Policy.ResolvedMCPServers {
		if trimmed := strings.TrimSpace(server); trimmed != "" {
			policy.Spec.AllowedServers = append(policy.Spec.AllowedServers, platformv1alpha1.MCPAllowedServer{Name: trimmed})
		}
	}
	return policy
}
