package configtest

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

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
	"algorand-vulnerability-scanner":  "building-secure-contracts",
	"cairo-vulnerability-scanner":     "building-secure-contracts",
	"code-maturity-assessor":          "building-secure-contracts",
	"guidelines-advisor":              "building-secure-contracts",
	"secure-workflow-guide":           "building-secure-contracts",
	"ton-vulnerability-scanner":       "building-secure-contracts",
	// Secure design and testing.
	"sharp-edges":            "sharp-edges",
	"property-based-testing": "property-based-testing",
	"address-sanitizer":      "testing-handbook-skills",
	"aflpp":                  "testing-handbook-skills",
	"constant-time-testing":  "testing-handbook-skills",
	"fuzzing-dictionary":     "testing-handbook-skills",
	"fuzzing-obstacles":      "testing-handbook-skills",
	"ossfuzz":                "testing-handbook-skills",
	"wycheproof":             "testing-handbook-skills",
}

type externalSkillSource struct {
	name, url, ref, path string
	attributionTokens    []string
}

var otherExternalSecuritySkills = []externalSkillSource{
	{
		name: "google-google-cloud-waf-security", url: "https://github.com/google/skills",
		ref: "092e210b243601797a0fb939040be2b1288e6d39", path: "skills/cloud/google-cloud-waf-security",
		attributionTokens: []string{"Google", "Apache-2.0"},
	},
	{
		name: "github-agent-governance", url: "https://github.com/github/awesome-copilot",
		ref: "3f0bba475ec40b9680e1d0311b9caffeec5ad4c3", path: "skills/agent-governance/SKILL.md",
		attributionTokens: []string{"GitHub", "MIT"},
	},
	{
		name: "github-agent-supply-chain", url: "https://github.com/github/awesome-copilot",
		ref: "3f0bba475ec40b9680e1d0311b9caffeec5ad4c3", path: "skills/agent-supply-chain/SKILL.md",
		attributionTokens: []string{"GitHub", "MIT"},
	},
}

// inlineSecuritySkills are the first-party bug-hunting companions to the
// security-scan handbook.
var inlineSecuritySkills = []string{
	"api-authz-hunting",
	"bug-bounty-reporting",
	"cloud-iac-hunting",
	"evm-economic-and-mev-review",
	"evm-low-level-and-deployment-review",
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
			if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(git.Ref) {
				t.Errorf("source.git.ref = %q, want an immutable commit SHA", git.Ref)
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

func TestOtherExternalSecuritySkillAssets(t *testing.T) {
	t.Parallel()

	for _, source := range otherExternalSecuritySkills {
		source := source
		t.Run(source.name, func(t *testing.T) {
			t.Parallel()
			var skill platformv1alpha1.Skill
			readBootstrapAsset(t, "skills", source.name, &skill)
			assertSkillAssetBasics(t, source.name, skill)

			git := skill.Spec.Source.Git
			if git == nil {
				t.Fatal("external skill must be git-sourced")
			}
			if git.URL != source.url || git.Ref != source.ref || git.Path != source.path {
				t.Errorf("git source = %q@%q:%q, want %q@%q:%q", git.URL, git.Ref, git.Path, source.url, source.ref, source.path)
			}
			if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(git.Ref) {
				t.Errorf("source.git.ref = %q, want an immutable commit SHA", git.Ref)
			}
			for _, token := range source.attributionTokens {
				if !strings.Contains(skill.Spec.Description, token) {
					t.Errorf("description must contain attribution token %q", token)
				}
			}
			contents, err := os.ReadFile(repoPath("configs", "skills", source.name+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(contents), source.url) {
				t.Error("asset must carry its upstream URL for provenance")
			}
		})
	}
}

func TestSkillBootstrapInventoryParity(t *testing.T) {
	t.Parallel()

	sourceDir := repoPath("configs", "skills")
	mirrorDir := repoPath("dist", "chart", "files", "bootstrap", "skills")
	sourceEntries, err := os.ReadDir(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	mirrorEntries, err := os.ReadDir(mirrorDir)
	if err != nil {
		t.Fatal(err)
	}

	sourceNames := make(map[string]bool)
	for _, entry := range sourceEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		sourceNames[entry.Name()] = true
		source, err := os.ReadFile(sourceDir + string(os.PathSeparator) + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		mirror, err := os.ReadFile(mirrorDir + string(os.PathSeparator) + entry.Name())
		if err != nil {
			t.Errorf("missing chart mirror for %s: %v", entry.Name(), err)
			continue
		}
		if !bytes.Equal(source, mirror) {
			t.Errorf("source and chart mirror differ for %s", entry.Name())
		}
		var skill platformv1alpha1.Skill
		if err := yaml.Unmarshal(source, &skill); err != nil {
			t.Errorf("decode %s: %v", entry.Name(), err)
			continue
		}
		bundleMember := skill.Annotations["platform.gratefulagents.dev/security-skill"] == "true"
		if entry.Name() == "grafana.yaml" {
			if bundleMember {
				t.Error("grafana must not be included in the opt-in security skill bundle")
			}
		} else if !bundleMember {
			t.Errorf("security skill %s is missing the opt-in bundle annotation", entry.Name())
		}
	}
	for _, entry := range mirrorEntries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") && !sourceNames[entry.Name()] {
			t.Errorf("chart mirror has no source asset: %s", entry.Name())
		}
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
