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
)

// skillNameRe accepts lowercase
// alphanumeric with single hyphen separators, max 64 chars (checked apart).
var skillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const (
	skillResourceType        = "skill"
	skillsShSourceAnnotation = "platform.gratefulagents.dev/skills-sh-source"
	// skillMaxInlineInstructionBytes matches the controller's SKILL.md cap so an
	// inline skill can never be larger than a fetched one.
	skillMaxInlineInstructionBytes = 256 * 1024
	skillsShIDAnnotation           = "platform.gratefulagents.dev/skills-sh-id"
	skillsShURLAnnotation          = "platform.gratefulagents.dev/skills-sh-url"
	skillsShHashAnnotation         = "platform.gratefulagents.dev/skills-sh-hash"
)

// githubTreeRe matches browser-style GitHub links to a folder at a ref:
// https://github.com/{owner}/{repo}/tree/{ref}/{path...}
var githubTreeRe = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/(?:tree|blob)/([^/]+)(?:/(.*))?$`)

// githubRepoRe matches a bare github.com repository URL, the only git source
// the Skill controller can fetch. Rejecting other hosts here turns a
// permanent controller Error into an immediate InvalidArgument.
var githubRepoRe = regexp.MustCompile(`^https?://(?:www\.)?github\.com/([^/\s]+)/([^/\s]+?)(?:\.git)?/?$`)

// validateSkillGitURL reports whether url names a github.com repository.
func validateSkillGitURL(url string) error {
	if !githubRepoRe.MatchString(strings.TrimSpace(url)) {
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("git_url must be a github.com repository or folder link (https://github.com/owner/repo[/tree/ref/path])"))
	}
	return nil
}

// validateSkillMCPServerRefs checks that every required MCP server has a
// valid name and exists in the caller's namespace, so a skill never advertises
// tools that can't be attached.
func (s *Server) validateSkillMCPServerRefs(ctx context.Context, namespace string, refs []platformv1alpha1.NamedRef) error {
	for _, ref := range refs {
		if !mcpServerNameRe.MatchString(ref.Name) {
			return connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("mcp server name %q must be lowercase letters, digits, and hyphens", ref.Name))
		}
		server := &platformv1alpha1.MCPServer{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, server); err != nil {
			if k8serrors.IsNotFound(err) {
				return connect.NewError(connect.CodeInvalidArgument,
					fmt.Errorf("mcp server %q not found in your namespace; create it under Resources → MCP servers first", ref.Name))
			}
			return mapK8sError("read MCP server", err)
		}
	}
	return nil
}

// ListSkills lists the skills (Skill CRDs) available in the caller's
// namespace, including resolved state for git-sourced skills.
func (s *Server) ListSkills(ctx context.Context, _ *platform.ListSkillsRequest) (*platform.ListSkillsResponse, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	var list platformv1alpha1.SkillList
	if err := s.k8sClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, mapK8sError("list skills", err)
	}
	resp := &platform.ListSkillsResponse{Namespace: namespace}
	for i := range list.Items {
		resp.Skills = append(resp.Skills, skillInfo(&list.Items[i]))
	}
	sort.Slice(resp.Skills, func(i, j int) bool { return resp.Skills[i].Name < resp.Skills[j].Name })
	return resp, nil
}

// UpsertSkill creates or updates a Skill CR in the caller's namespace.
// Exactly one of instructions (inline) or git_url (git source) must be set;
// "install from GitHub" is an upsert with a git source. Pasted browser links
// of the form github.com/{owner}/{repo}/tree/{ref}/{path} are normalized into
// url+ref+path automatically.
func (s *Server) UpsertSkill(ctx context.Context, req *platform.UpsertSkillRequest) (*platform.SkillInfo, error) {
	if err := requireMemberActor(ctx, "create or update skills"); err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(req.GetName()))
	if name == "" || len(name) > 64 || !skillNameRe.MatchString(name) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("skill name %q must be lowercase alphanumeric with single hyphen separators (max 64 chars)", req.GetName()))
	}

	instructions := strings.TrimSpace(req.GetInstructions())
	gitURL := strings.TrimSpace(req.GetGitUrl())
	if (instructions == "") == (gitURL == "") {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("provide exactly one of instructions (inline skill) or git_url (GitHub-sourced skill)"))
	}
	if desc := strings.TrimSpace(req.GetDescription()); len(desc) > 1024 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("description exceeds 1024 characters"))
	}
	if len(instructions) > skillMaxInlineInstructionBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("instructions are %d bytes (max %d)", len(instructions), skillMaxInlineInstructionBytes))
	}

	spec := platformv1alpha1.SkillSpec{
		Version:     strings.TrimSpace(req.GetVersion()),
		Description: strings.TrimSpace(req.GetDescription()),
	}
	if instructions != "" {
		spec.Source.Inline = &platformv1alpha1.SkillInlineSource{Instructions: instructions}
	} else {
		url, ref, path := normalizeGitHubSkillLink(gitURL, strings.TrimSpace(req.GetGitRef()), strings.TrimSpace(req.GetGitPath()))
		if err := validateSkillGitURL(url); err != nil {
			return nil, err
		}
		spec.Source.Git = &platformv1alpha1.SkillGitSource{URL: url, Ref: ref, Path: path}
	}
	if servers := namedRefsFromNames(req.GetMcpServerRefs()); len(servers) > 0 {
		if err := s.validateSkillMCPServerRefs(ctx, namespace, servers); err != nil {
			return nil, err
		}
		spec.Requires = &platformv1alpha1.SkillRequires{MCPServers: servers}
	}

	skill := &platformv1alpha1.Skill{}
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := s.k8sClient.Get(ctx, key, skill); err != nil {
		if !k8serrors.IsNotFound(err) {
			return nil, mapK8sError("read skill", err)
		}
		skill = &platformv1alpha1.Skill{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       spec,
		}
		if err := s.k8sClient.Create(ctx, skill); err != nil {
			return nil, mapK8sError("create skill", err)
		}
		return skillInfo(skill), nil
	}
	if skill.Annotations[skillsShSourceAnnotation] != "" && skillSourceChanged(skill.Spec.Source, spec.Source) {
		// Manual instruction edits or a switch to a git source detach the
		// resource from its immutable catalog snapshot; retaining the old
		// source/hash would misrepresent provenance.
		delete(skill.Annotations, skillsShSourceAnnotation)
		delete(skill.Annotations, skillsShIDAnnotation)
		delete(skill.Annotations, skillsShURLAnnotation)
		delete(skill.Annotations, skillsShHashAnnotation)
	}
	skill.Spec = spec
	if err := s.k8sClient.Update(ctx, skill); err != nil {
		return nil, mapK8sError("update skill", err)
	}
	return skillInfo(skill), nil
}

// DeleteSkill removes a Skill from the caller's namespace. Refs to missing
// skills are skipped at run time.
func (s *Server) DeleteSkill(ctx context.Context, req *platform.DeleteSkillRequest) error {
	if err := requireMemberActor(ctx, "delete skills"); err != nil {
		return err
	}
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return err
	}
	name := strings.ToLower(strings.TrimSpace(req.GetName()))
	if name == "" || len(name) > 64 || !skillNameRe.MatchString(name) {
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("skill name %q must be lowercase alphanumeric with single hyphen separators (max 64 chars)", req.GetName()))
	}
	skill := &platformv1alpha1.Skill{}
	skill.Name = name
	skill.Namespace = namespace
	if err := s.k8sClient.Delete(ctx, skill); err != nil && !k8serrors.IsNotFound(err) {
		return mapK8sError("delete skill", err)
	}
	return nil
}

// skillSourceChanged reports whether an update replaces the content a catalog
// install pinned: different inline instructions, or any switch to a git source.
func skillSourceChanged(current, next platformv1alpha1.SkillSource) bool {
	if next.Git != nil {
		return true
	}
	if next.Inline == nil {
		return false
	}
	return current.Inline == nil || current.Inline.Instructions != next.Inline.Instructions
}

// skillInfo converts a Skill CR into its wire form, including resolved state
// for git-sourced skills.
func skillInfo(skill *platformv1alpha1.Skill) *platform.SkillInfo {
	info := &platform.SkillInfo{
		Name:        skill.Name,
		Version:     skill.Spec.Version,
		Description: strings.TrimSpace(skill.Spec.Description),
		Phase:       skill.Status.Phase,
	}
	if inline := skill.Spec.Source.Inline; inline != nil {
		info.Instructions = inline.Instructions
	}
	if git := skill.Spec.Source.Git; git != nil {
		info.GitUrl = git.URL
		info.GitRef = git.Ref
		info.GitPath = git.Path
	}
	if req := skill.Spec.Requires; req != nil {
		for _, ref := range req.MCPServers {
			info.McpServerRefs = append(info.McpServerRefs, ref.Name)
		}
	}
	if res := skill.Status.Resolved; res != nil {
		info.ResolvedName = res.Name
		info.ResolvedDescription = res.Description
		info.ResolvedSha = res.SHA
	}
	for _, cond := range skill.Status.Conditions {
		if cond.Type == "Resolved" && cond.Status == metav1.ConditionFalse {
			info.StatusMessage = cond.Message
		}
	}
	info.CatalogSource = skill.Annotations[skillsShSourceAnnotation]
	info.CatalogSkillId = skill.Annotations[skillsShIDAnnotation]
	info.CatalogUrl = skill.Annotations[skillsShURLAnnotation]
	info.CatalogHash = skill.Annotations[skillsShHashAnnotation]
	return info
}

// normalizeGitHubSkillLink turns a pasted GitHub browser link into repo
// url + ref + path. Explicit ref/path values win over link-derived ones.
// Examples:
//
//	https://github.com/anthropics/skills/tree/main/document-skills/pdf
//	  → url=https://github.com/anthropics/skills ref=main path=document-skills/pdf
//	https://github.com/owner/repo → unchanged
func normalizeGitHubSkillLink(rawURL, ref, path string) (string, string, string) {
	m := githubTreeRe.FindStringSubmatch(strings.TrimSuffix(rawURL, "/"))
	if m == nil {
		return rawURL, ref, path
	}
	url := "https://github.com/" + m[1] + "/" + m[2]
	if ref == "" {
		ref = m[3]
	}
	if path == "" {
		path = strings.TrimSuffix(m[4], "/SKILL.md")
	}
	return url, ref, path
}
