/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SkillRefreshAnnotation forces a re-fetch of a git-sourced skill when its
// value changes (e.g. set it to the current timestamp).
const SkillRefreshAnnotation = "platform.gratefulagents.dev/refresh"

// SkillInlineSource carries the skill instructions directly in the spec.
type SkillInlineSource struct {
	// instructions is the prompt guidance injected into the system prompt of
	// any run this skill is attached to. Keep it concise: it costs context on
	// every turn.
	// +kubebuilder:validation:MinLength=1
	Instructions string `json:"instructions"`
}

// SkillGitSource points at a git repository folder containing a SKILL.md file
// in the Anthropic agent-skills format (YAML frontmatter with name and
// description, body with the instructions). Public repositories only for now.
type SkillGitSource struct {
	// url is the repository URL (e.g. https://github.com/anthropics/skills).
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
	// ref is the branch, tag, or commit SHA to fetch (default branch if empty).
	// +optional
	Ref string `json:"ref,omitempty"`
	// path is the folder within the repository that contains SKILL.md (repo
	// root if empty). The folder name must match the skill name from the
	// SKILL.md frontmatter.
	// +optional
	Path string `json:"path,omitempty"`
}

// SkillSource identifies where the skill's instructions come from.
// Exactly one of inline or git must be set.
// +kubebuilder:validation:XValidation:rule="(has(self.inline) ? 1 : 0) + (has(self.git) ? 1 : 0) == 1",message="exactly one of inline or git must be set"
type SkillSource struct {
	// +optional
	Inline *SkillInlineSource `json:"inline,omitempty"`
	// +optional
	Git *SkillGitSource `json:"git,omitempty"`
}

// SkillRequires declares resources a run needs for this skill to be useful.
type SkillRequires struct {
	// mcpServers lists MCPServer resources (same namespace) this skill
	// teaches. Attaching the skill to a run auto-attaches these servers.
	// +listType=atomic
	// +optional
	MCPServers []NamedRef `json:"mcpServers,omitempty"`
}

// SkillSpec defines the desired state of Skill.
// A Skill is reusable prompt guidance ("how to do X well") that can be
// attached to AgentRuns through spec.skillRefs. Skills may require MCP
// servers, which are auto-attached alongside the skill.
type SkillSpec struct {
	// +optional
	Version string `json:"version,omitempty"`
	// description is a short human-readable summary shown when browsing
	// skills. For git-sourced skills the resolved SKILL.md frontmatter
	// description is used when this is empty.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Description string `json:"description,omitempty"`
	// +required
	Source SkillSource `json:"source"`
	// +optional
	Requires *SkillRequires `json:"requires,omitempty"`
}

// SkillResolved is the materialized content of a skill after its source has
// been fetched and validated (populated by the controller; for inline skills
// it mirrors the spec).
type SkillResolved struct {
	// name is the skill name from SKILL.md frontmatter (git) or the object
	// name (inline).
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Description string `json:"description,omitempty"`
	// instructions is the full skill body injected into runs.
	// +optional
	Instructions string `json:"instructions,omitempty"`
	// sha is the resolved commit SHA for git-sourced skills.
	// +optional
	SHA string `json:"sha,omitempty"`
	// +optional
	SyncedAt *metav1.Time `json:"syncedAt,omitempty"`
}

// SkillStatus defines the observed state of Skill.
type SkillStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	Resolved *SkillResolved `json:"resolved,omitempty"`
	// observedGeneration is the spec generation the resolved content was
	// produced from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// lastRefresh records the refresh annotation value that triggered the
	// last fetch (git-sourced skills re-fetch when the annotation changes).
	// +optional
	LastRefresh string `json:"lastRefresh,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Skill is the Schema for reusable agent skills (inline or git-sourced
// SKILL.md instruction packages).
type Skill struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SkillSpec `json:"spec"`

	// +optional
	Status SkillStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SkillList contains a list of Skill.
type SkillList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Skill `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Skill{}, &SkillList{})
}
