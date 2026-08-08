package configtest

import (
	"os"
	"regexp"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// trailOfBitsPrefix marks skills sourced from the external Trail of Bits
// repository. The suffix must equal the upstream skill folder name, because a
// git-sourced skill resolves SKILL.md from that folder and the folder name has
// to match the frontmatter skill name.
const trailOfBitsPrefix = "trail-of-bits-"

const trailOfBitsRepo = "https://github.com/trailofbits/skills"

// externalSecuritySkills are the curated Trail of Bits skills shipped as git
// sourced Skill assets, mapped to the upstream plugin that owns each skill.
var externalSecuritySkills = map[string]string{
	// Audit methodology.
	"audit-context-building":        "audit-context-building",
	"entry-point-analyzer":          "entry-point-analyzer",
	"variant-analysis":              "variant-analysis",
	"fp-check":                      "fp-check",
	"vulnerability-triage-brocards": "vulnerability-triage-brocards",
	"differential-review":           "differential-review",
	// Language and cryptography review.
	"c-review":               "c-review",
	"rust-review":            "rust-review",
	"constant-time-analysis": "constant-time-analysis",
	"zeroize-audit":          "zeroize-audit",
	"dimensional-analysis":   "dimensional-analysis",
	// Static analysis tooling.
	"semgrep":                      "static-analysis",
	"codeql":                       "static-analysis",
	"sarif-parsing":                "static-analysis",
	"semgrep-rule-creator":         "semgrep-rule-creator",
	"semgrep-rule-variant-creator": "semgrep-rule-variant-creator",
	// Fuzzing and dynamic testing.
	"harness-writing":   "testing-handbook-skills",
	"libfuzzer":         "testing-handbook-skills",
	"cargo-fuzz":        "testing-handbook-skills",
	"atheris":           "testing-handbook-skills",
	"coverage-analysis": "testing-handbook-skills",
	// Supply chain and offensive tooling.
	"supply-chain-risk-auditor": "supply-chain-risk-auditor",
	"agentic-actions-auditor":   "agentic-actions-auditor",
	"yara-rule-authoring":       "yara-authoring",
	"burpsuite-project-parser":  "burpsuite-project-parser",
	// Blockchain.
	"solana-vulnerability-scanner":    "building-secure-contracts",
	"cosmos-vulnerability-scanner":    "building-secure-contracts",
	"substrate-vulnerability-scanner": "building-secure-contracts",
	"token-integration-analyzer":      "building-secure-contracts",
	"audit-prep-assistant":            "building-secure-contracts",
}

// inlineSecuritySkills are the first-party bug-hunting companions to the
// security-scan handbook.
var inlineSecuritySkills = []string{
	"api-authz-hunting",
	"bug-bounty-reporting",
	"cloud-iac-hunting",
	"exploit-poc-discipline",
	"kubernetes-operator-hunting",
	"web-app-hunting",
}

// dns1123Label matches the names Kubernetes accepts for these objects; a skill
// asset with an invalid name is rejected at bootstrap time.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// gitSkillPath matches the upstream layout plugins/<plugin>/skills/<skill>.
var gitSkillPath = regexp.MustCompile(`^plugins/[a-z0-9][-a-z0-9]*/skills/[a-z0-9][-a-z0-9]*$`)

// assertSkillAssetBasics enforces what every shipped skill must satisfy,
// regardless of where its instructions come from.
func assertSkillAssetBasics(t *testing.T, name string, skill platformv1alpha1.Skill) {
	t.Helper()

	if skill.Name != name {
		t.Fatalf("metadata.name = %q, want %q", skill.Name, name)
	}
	if !dns1123Label.MatchString(skill.Name) || len(skill.Name) > 63 {
		t.Errorf("metadata.name = %q is not a valid DNS-1123 label", skill.Name)
	}
	description := strings.TrimSpace(skill.Spec.Description)
	if description == "" {
		t.Error("skill must carry a description for the catalog")
	}
	// Mirrors the MaxLength=1024 marker on SkillSpec.Description: a longer
	// description is rejected by the API server at bootstrap.
	if len(skill.Spec.Description) > 1024 {
		t.Errorf("description is %d chars, want <= 1024", len(skill.Spec.Description))
	}
	inline, git := skill.Spec.Source.Inline != nil, skill.Spec.Source.Git != nil
	if inline == git {
		t.Fatalf("exactly one of source.inline/source.git must be set (inline=%v git=%v)", inline, git)
	}
}

func TestExternalSecuritySkillAssets(t *testing.T) {
	t.Parallel()

	for skillFolder, plugin := range externalSecuritySkills {
		t.Run(skillFolder, func(t *testing.T) {
			t.Parallel()

			name := trailOfBitsPrefix + skillFolder
			var skill platformv1alpha1.Skill
			readBootstrapAsset(t, "skills", name, &skill)
			assertSkillAssetBasics(t, name, skill)

			git := skill.Spec.Source.Git
			if git == nil {
				t.Fatal("curated external skills must be git-sourced")
			}
			if git.URL != trailOfBitsRepo {
				t.Errorf("source.git.url = %q, want %q", git.URL, trailOfBitsRepo)
			}
			if git.Ref == "" {
				t.Error("source.git.ref must pin a branch, tag, or SHA")
			}
			if !gitSkillPath.MatchString(git.Path) {
				t.Fatalf("source.git.path = %q, want plugins/<plugin>/skills/<skill>", git.Path)
			}
			// The fetched folder name has to match the SKILL.md frontmatter
			// name, so the CR name minus the provenance prefix must equal the
			// last path segment or the resolved skill is misfiled.
			if want := "plugins/" + plugin + "/skills/" + skillFolder; git.Path != want {
				t.Errorf("source.git.path = %q, want %q", git.Path, want)
			}
			// The license requires attribution wherever the skill is
			// redistributed, so provenance lives in the description too.
			if !strings.Contains(skill.Spec.Description, "Trail of Bits") ||
				!strings.Contains(skill.Spec.Description, "CC-BY-SA-4.0") {
				t.Errorf("description must attribute the upstream skill and its license, got %q", skill.Spec.Description)
			}
			source, err := os.ReadFile(repoPath("configs", "skills", name+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(source), trailOfBitsRepo) {
				t.Error("asset must carry an upstream URL comment for attribution")
			}
		})
	}
}

func TestInlineSecuritySkillAssets(t *testing.T) {
	t.Parallel()

	for _, name := range inlineSecuritySkills {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var skill platformv1alpha1.Skill
			readBootstrapAsset(t, "skills", name, &skill)
			assertSkillAssetBasics(t, name, skill)

			inline := skill.Spec.Source.Inline
			if inline == nil {
				t.Fatal("first-party hunting skills must ship inline instructions")
			}
			instructions := strings.TrimSpace(inline.Instructions)
			// These are handbooks, not one-liners: a stub would silently
			// weaken every scan that attaches the skill.
			if len(strings.Split(instructions, "\n")) < 20 || len(instructions) < 1000 {
				t.Errorf("instructions look like a stub (%d bytes)", len(instructions))
			}
			if !strings.Contains(instructions, "## ") {
				t.Error("instructions must be structured with markdown sections")
			}
		})
	}
}
