package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	defaultSkillsCatalogURL = "https://skills.sh"
	catalogContentLimit     = 256 * 1024
	catalogInstallTimeout   = 30 * time.Second
)

var (
	catalogDomainRe          = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?\.[a-z]{2,}$`)
	catalogSourceComponentRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	catalogSkillIDRe         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type skillsCatalogAPIResponse struct {
	Skills  []skillsCatalogAPIEntry `json:"skills"`
	Total   int64                   `json:"total"`
	Count   int64                   `json:"count"`
	HasMore bool                    `json:"hasMore"`
	Page    int32                   `json:"page"`
}

type skillsCatalogAPIEntry struct {
	Source     string `json:"source"`
	SkillID    string `json:"skillId"`
	Name       string `json:"name"`
	Installs   int64  `json:"installs"`
	IsOfficial bool   `json:"isOfficial"`
}

var defaultSkillsCatalogHTTPClient = newPublicHTTPSClient()

func (s *Server) skillsCatalogClient() *http.Client {
	if s.skillsHTTP != nil {
		return s.skillsHTTP
	}
	return defaultSkillsCatalogHTTPClient
}

// newPublicHTTPSClient pins each connection to a DNS result that was checked
// to be publicly routable. Redirects are rejected so a catalog provider cannot
// bounce an operator-side request into cluster, loopback, or metadata networks.
func newPublicHTTPSClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxResponseHeaderBytes = 64 * 1024
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if port != "443" {
			return nil, fmt.Errorf("catalog egress only permits HTTPS port 443")
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("catalog host %q has no IP addresses", host)
		}
		for _, address := range addresses {
			if !isPublicCatalogIP(address.IP) {
				return nil, fmt.Errorf("catalog host %q resolves to a non-public address", host)
			}
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		var lastErr error
		for _, address := range addresses {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

var catalogBlockedNetworks = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"2001:db8::/32",
	}
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, _ := net.ParseCIDR(cidr)
		networks = append(networks, network)
	}
	return networks
}()

func isPublicCatalogIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, network := range catalogBlockedNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

func (s *Server) skillsCatalogBaseURL() string {
	if s.skillsCatalogURL != "" {
		return s.skillsCatalogURL
	}
	return defaultSkillsCatalogURL
}

// ListSkillCatalog proxies the public skills.sh catalog API so the browser does
// not need to depend on third-party CORS behavior. Blank queries browse all
// skills in all-time order; non-blank queries use skills.sh search.
func (s *Server) ListSkillCatalog(ctx context.Context, req *platform.ListSkillCatalogRequest) (*platform.ListSkillCatalogResponse, error) {
	page := req.GetPage()
	if page < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("page must be non-negative"))
	}
	query := strings.TrimSpace(req.GetQuery())
	endpoint := fmt.Sprintf("%s/api/skills/all-time/%d", s.skillsCatalogBaseURL(), page)
	if query != "" {
		endpoint = fmt.Sprintf("%s/api/search?q=%s&limit=100", s.skillsCatalogBaseURL(), url.QueryEscape(query))
		page = 0
	}
	var result skillsCatalogAPIResponse
	if err := s.getCatalogJSON(ctx, endpoint, &result); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("loading skills.sh catalog: %w", err))
	}
	resp := &platform.ListSkillCatalogResponse{Total: result.Total, HasMore: result.HasMore, Page: page}
	if query != "" {
		resp.Total = result.Count
	}
	for _, entry := range result.Skills {
		if validCatalogCoordinates(entry.Source, entry.SkillID) {
			resp.Skills = append(resp.Skills, catalogEntryInfo(entry))
		}
	}
	return resp, nil
}

// InstallSkillFromCatalog downloads and validates a skills.sh catalog entry,
// then creates an inline Skill CRD in the authenticated user's namespace.
func (s *Server) InstallSkillFromCatalog(ctx context.Context, req *platform.InstallSkillFromCatalogRequest) (*platform.SkillInfo, error) {
	source := strings.TrimSpace(req.GetSource())
	skillID := strings.TrimSpace(req.GetSkillId())
	if !validCatalogCoordinates(source, skillID) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid skills.sh source or skill id"))
	}
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, catalogInstallTimeout)
	defer cancel()

	contents, catalogHash, err := s.downloadCatalogSkill(ctx, source, skillID)
	if err != nil {
		if errors.Is(err, errCatalogSkillNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("skill %s/%s was not found in skills.sh", source, skillID))
		}
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("downloading %s from skills.sh source %s: %w", skillID, source, err))
	}
	frontmatter, instructions, err := parseCatalogSkillMD(contents)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if frontmatter.Name != skillID {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("SKILL.md name %q does not match catalog skill id %q", frontmatter.Name, skillID))
	}
	name, err := s.catalogResourceName(ctx, namespace, source, skillID)
	if err != nil {
		return nil, err
	}
	skill := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace,
			Annotations: map[string]string{
				skillsShSourceAnnotation: source,
				skillsShIDAnnotation:     skillID,
				skillsShURLAnnotation:    catalogURL(source, skillID),
				skillsShHashAnnotation:   catalogHash,
			},
		},
		Spec: platformv1alpha1.SkillSpec{
			Description: strings.TrimSpace(frontmatter.Description),
			Source: platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{
				Instructions: instructions,
			}},
		},
	}
	if err := s.k8sClient.Create(ctx, skill); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("skill %q is already installed", skillID))
		}
		return nil, mapK8sError("install skill", err)
	}
	return skillInfo(skill), nil
}

func (s *Server) getCatalogJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gratefulagents")
	resp, err := s.skillsCatalogClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(target)
}

func catalogEntryInfo(entry skillsCatalogAPIEntry) *platform.SkillCatalogEntry {
	return &platform.SkillCatalogEntry{
		Source: entry.Source, SkillId: entry.SkillID, Name: entry.Name,
		Installs: entry.Installs, IsOfficial: entry.IsOfficial,
		CatalogUrl: catalogURL(entry.Source, entry.SkillID),
	}
}

func catalogURL(source, skillID string) string {
	if catalogDomainRe.MatchString(source) {
		return defaultSkillsCatalogURL + "/site/" + source + "/" + skillID
	}
	return defaultSkillsCatalogURL + "/" + source + "/" + skillID
}

func validCatalogCoordinates(source, skillID string) bool {
	if source == "" || !catalogSkillIDRe.MatchString(skillID) ||
		strings.ContainsAny(source, "?#\\") || strings.Contains(source, "..") {
		return false
	}
	if catalogDomainRe.MatchString(source) {
		return true
	}
	parts := strings.Split(source, "/")
	return len(parts) == 2 && catalogSourceComponentRe.MatchString(parts[0]) &&
		catalogSourceComponentRe.MatchString(parts[1])
}

type catalogFrontmatter struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func parseCatalogSkillMD(content string) (catalogFrontmatter, string, error) {
	var frontmatter catalogFrontmatter
	if len(content) > catalogContentLimit {
		return frontmatter, "", fmt.Errorf("SKILL.md exceeds %d bytes", catalogContentLimit)
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return frontmatter, "", fmt.Errorf("SKILL.md must start with YAML frontmatter")
	}
	rest := normalized[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return frontmatter, "", fmt.Errorf("SKILL.md frontmatter is not terminated")
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), &frontmatter); err != nil {
		return frontmatter, "", fmt.Errorf("invalid SKILL.md frontmatter: %w", err)
	}
	body := strings.TrimSpace(strings.TrimPrefix(rest[end+len("\n---"):], "\n"))
	if frontmatter.Name == "" || frontmatter.Description == "" || body == "" {
		return frontmatter, "", fmt.Errorf("SKILL.md requires name, description, and instructions")
	}
	if len(frontmatter.Description) > 1024 {
		return frontmatter, "", fmt.Errorf("SKILL.md description exceeds 1024 characters")
	}
	return frontmatter, body, nil
}

var errCatalogSkillNotFound = errors.New("catalog skill not found")

type catalogArtifact struct {
	Files []catalogArtifactFile `json:"files"`
	Hash  string                `json:"hash"`
}

type catalogArtifactFile struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

func (s *Server) downloadCatalogSkill(ctx context.Context, source, skillID string) (string, string, error) {
	if catalogDomainRe.MatchString(source) {
		if err := s.requireCatalogPage(ctx, source, skillID); err != nil {
			return "", "", err
		}
		endpoint := "https://" + source + "/.well-known/skills/" + url.PathEscape(skillID) + "/SKILL.md"
		contents, err := s.fetchCatalogText(ctx, endpoint)
		if err != nil {
			return "", "", err
		}
		sum := sha256.Sum256([]byte(contents))
		return contents, hex.EncodeToString(sum[:]), nil
	}
	parts := strings.Split(source, "/")
	endpoint := fmt.Sprintf("%s/api/download/%s/%s/%s", s.skillsCatalogBaseURL(),
		url.PathEscape(parts[0]), url.PathEscape(parts[1]), url.PathEscape(skillID))
	var artifact catalogArtifact
	status, err := s.getCatalogJSONStatus(ctx, endpoint, &artifact, 16*1024*1024)
	if err != nil {
		return "", "", err
	}
	if status == http.StatusNotFound {
		return "", "", errCatalogSkillNotFound
	}
	if status != http.StatusOK {
		return "", "", fmt.Errorf("skills.sh download returned %s", http.StatusText(status))
	}
	if len(artifact.Hash) != 64 {
		return "", "", fmt.Errorf("skills.sh artifact has an invalid content hash")
	}
	if _, err := hex.DecodeString(artifact.Hash); err != nil {
		return "", "", fmt.Errorf("skills.sh artifact has an invalid content hash")
	}
	for _, file := range artifact.Files {
		if strings.EqualFold(strings.TrimPrefix(file.Path, "./"), "SKILL.md") {
			return file.Contents, artifact.Hash, nil
		}
	}
	return "", "", fmt.Errorf("skills.sh artifact does not contain SKILL.md")
}

func (s *Server) requireCatalogPage(ctx context.Context, source, skillID string) error {
	endpoint := fmt.Sprintf("%s/site/%s/%s", s.skillsCatalogBaseURL(), url.PathEscape(source), url.PathEscape(skillID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "gratefulagents")
	resp, err := s.skillsCatalogClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errCatalogSkillNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("skills.sh catalog page returned %s", resp.Status)
	}
	return nil
}

func (s *Server) fetchCatalogText(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain, application/octet-stream")
	req.Header.Set("User-Agent", "gratefulagents")
	resp, err := s.skillsCatalogClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}
	contents, err := io.ReadAll(io.LimitReader(resp.Body, catalogContentLimit+1))
	if err != nil {
		return "", err
	}
	if len(contents) > catalogContentLimit {
		return "", fmt.Errorf("SKILL.md exceeds %d bytes", catalogContentLimit)
	}
	return string(contents), nil
}

func (s *Server) getCatalogJSONStatus(ctx context.Context, endpoint string, target any, limit int64) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gratefulagents")
	resp, err := s.skillsCatalogClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(target); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

var invalidResourceNameChars = regexp.MustCompile(`[^a-z0-9]+`)

func catalogResourceNameBase(skillID string) string {
	name := invalidResourceNameChars.ReplaceAllString(strings.ToLower(skillID), "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = "skill"
	}
	if len(name) > 64 {
		name = strings.TrimRight(name[:64], "-")
	}
	return name
}

func (s *Server) catalogResourceName(ctx context.Context, namespace, source, skillID string) (string, error) {
	base := catalogResourceNameBase(skillID)
	existing := &platformv1alpha1.Skill{}
	err := s.k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: base}, existing)
	if k8serrors.IsNotFound(err) {
		return base, nil
	}
	if err != nil {
		return "", mapK8sError("check installed skill", err)
	}
	if existing.Annotations[skillsShSourceAnnotation] == source && existing.Annotations[skillsShIDAnnotation] == skillID {
		return "", connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("skill %q from %s is already installed", skillID, source))
	}
	sum := sha256.Sum256([]byte(source + "/" + skillID))
	suffix := "-" + hex.EncodeToString(sum[:4])
	if len(base)+len(suffix) > 64 {
		base = strings.TrimRight(base[:64-len(suffix)], "-")
	}
	name := base + suffix
	existing = &platformv1alpha1.Skill{}
	err = s.k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing)
	if k8serrors.IsNotFound(err) {
		return name, nil
	}
	if err != nil {
		return "", mapK8sError("check installed skill", err)
	}
	if existing.Annotations[skillsShSourceAnnotation] == source && existing.Annotations[skillsShIDAnnotation] == skillID {
		return "", connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("skill %q from %s is already installed", skillID, source))
	}
	return "", connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("generated skill name %q is already in use", name))
}
