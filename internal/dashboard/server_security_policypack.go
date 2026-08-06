package dashboard

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// --- SecurityPolicyPack ---

// securityPolicyPackRetentionFromProto converts a retention block; day
// bounds are validated by ValidateSecurityPolicyPackSpec. A nil/empty proto
// yields nil (keep everything forever).
func securityPolicyPackRetentionFromProto(pb *platform.SecurityPolicyPackRetentionConfig) *triggersv1alpha1.SecurityPolicyPackRetention {
	if pb == nil {
		return nil
	}
	retention := &triggersv1alpha1.SecurityPolicyPackRetention{
		ScanDays:       pb.GetScanDays(),
		FindingDays:    pb.GetFindingDays(),
		ReportDays:     pb.GetReportDays(),
		EvidenceDays:   pb.GetEvidenceDays(),
		PoCDays:        pb.GetPocDays(),
		AuditEventDays: pb.GetAuditEventDays(),
	}
	if retention.IsZero() {
		return nil
	}
	return retention
}

// securityPolicyPackRetentionToProto converts a retention block for the
// resource proto. Nil/empty retention yields nil.
func securityPolicyPackRetentionToProto(r *triggersv1alpha1.SecurityPolicyPackRetention) *platform.SecurityPolicyPackRetentionConfig {
	if r == nil || r.IsZero() {
		return nil
	}
	return &platform.SecurityPolicyPackRetentionConfig{
		ScanDays:       r.ScanDays,
		FindingDays:    r.FindingDays,
		ReportDays:     r.ReportDays,
		EvidenceDays:   r.EvidenceDays,
		PocDays:        r.PoCDays,
		AuditEventDays: r.AuditEventDays,
	}
}

func securityPolicyPackSpecFromProto(pb *platform.SecurityPolicyPackResource) (triggersv1alpha1.SecurityPolicyPackSpec, error) {
	if pb == nil {
		return triggersv1alpha1.SecurityPolicyPackSpec{}, invalidArgument("policy pack is required")
	}
	if err := validateResourceName(pb.GetName()); err != nil {
		return triggersv1alpha1.SecurityPolicyPackSpec{}, err
	}
	dedupe, err := securityScanDedupeFromProto(pb.GetDedupe())
	if err != nil {
		return triggersv1alpha1.SecurityPolicyPackSpec{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	rankerRefs, err := securityResourceRefsFromProto("default_ranker_refs", pb.GetDefaultRankerRefs())
	if err != nil {
		return triggersv1alpha1.SecurityPolicyPackSpec{}, err
	}
	postScriptRefs, err := securityResourceRefsFromProto("default_post_script_refs", pb.GetDefaultPostScriptRefs())
	if err != nil {
		return triggersv1alpha1.SecurityPolicyPackSpec{}, err
	}
	budgets, err := securityScanBudgetsFromProto(pb.GetBudgets())
	if err != nil {
		return triggersv1alpha1.SecurityPolicyPackSpec{}, err
	}
	spec := triggersv1alpha1.SecurityPolicyPackSpec{
		Description:            strings.TrimSpace(pb.GetDescription()),
		RequiredCategories:     trimmedNonEmpty(pb.GetRequiredCategories()),
		MinSeverity:            strings.TrimSpace(pb.GetMinSeverity()),
		FailOnSeverity:         strings.TrimSpace(pb.GetFailOnSeverity()),
		Dedupe:                 dedupe,
		AllowedRuntimeProfiles: trimmedNonEmpty(pb.GetAllowedRuntimeProfiles()),
		DefaultRankerRefs:      rankerRefs,
		DefaultPostScriptRefs:  postScriptRefs,
		Enforced:               trimmedNonEmpty(pb.GetEnforced()),
		Budgets:                budgets,
		Retention:              securityPolicyPackRetentionFromProto(pb.GetRetention()),
	}
	for _, rule := range pb.GetSuppressions() {
		suppression := triggersv1alpha1.SecurityPolicySuppression{
			Name:   strings.TrimSpace(rule.GetName()),
			Reason: strings.TrimSpace(rule.GetReason()),
			Owner:  strings.TrimSpace(rule.GetOwner()),
			Matcher: triggersv1alpha1.SecuritySuppressionMatcher{
				Category:    strings.TrimSpace(rule.GetMatcher().GetCategory()),
				CWE:         strings.TrimSpace(rule.GetMatcher().GetCwe()),
				PathGlob:    strings.TrimSpace(rule.GetMatcher().GetPathGlob()),
				Fingerprint: strings.TrimSpace(rule.GetMatcher().GetFingerprint()),
			},
		}
		if rule.GetExpiresAt() != nil {
			expires := metav1.NewTime(rule.GetExpiresAt().AsTime())
			suppression.ExpiresAt = &expires
		}
		spec.Suppressions = append(spec.Suppressions, suppression)
	}
	if errs := triggersv1alpha1.ValidateSecurityPolicyPackSpec(spec); len(errs) != 0 {
		return triggersv1alpha1.SecurityPolicyPackSpec{}, securityLibraryInvalidArgument(errs)
	}
	return spec, nil
}

func securityPolicyPackToProto(cr *triggersv1alpha1.SecurityPolicyPack, referencing []string) *platform.SecurityPolicyPackResource {
	pb := &platform.SecurityPolicyPackResource{
		Namespace:              cr.Namespace,
		Name:                   cr.Name,
		Description:            cr.Spec.Description,
		RequiredCategories:     append([]string(nil), cr.Spec.RequiredCategories...),
		MinSeverity:            cr.Spec.MinSeverity,
		FailOnSeverity:         cr.Spec.FailOnSeverity,
		AllowedRuntimeProfiles: append([]string(nil), cr.Spec.AllowedRuntimeProfiles...),
		Enforced:               append([]string(nil), cr.Spec.Enforced...),
		Retention:              securityPolicyPackRetentionToProto(cr.Spec.Retention),
		Budgets:                securityScanBudgetsToProto(cr.Spec.Budgets),
		UsageCount:             int32(len(referencing)), //nolint:gosec // scan counts stay far below int32 bounds
		ReferencingScans:       referencing,
		Generation:             cr.Generation,
		CreatedAtUnix:          cr.CreationTimestamp.Unix(),
	}
	if d := cr.Spec.Dedupe; d != nil {
		pb.Dedupe = &platform.SecurityScanDedupeConfig{
			Enabled:                     d.Enabled == nil || *d.Enabled,
			SimilarityThresholdPermille: d.SimilarityThresholdPermille,
		}
	}
	for _, ref := range cr.Spec.DefaultRankerRefs {
		pb.DefaultRankerRefs = append(pb.DefaultRankerRefs, ref.Name)
	}
	for _, ref := range cr.Spec.DefaultPostScriptRefs {
		pb.DefaultPostScriptRefs = append(pb.DefaultPostScriptRefs, ref.Name)
	}
	for _, rule := range cr.Spec.Suppressions {
		pbRule := &platform.SecurityPolicySuppressionConfig{
			Name:   rule.Name,
			Reason: rule.Reason,
			Owner:  rule.Owner,
			Matcher: &platform.SecuritySuppressionMatcherConfig{
				Category:    rule.Matcher.Category,
				Cwe:         rule.Matcher.CWE,
				PathGlob:    rule.Matcher.PathGlob,
				Fingerprint: rule.Matcher.Fingerprint,
			},
		}
		if rule.ExpiresAt != nil {
			pbRule.ExpiresAt = timestamppb.New(rule.ExpiresAt.Time)
		}
		pb.Suppressions = append(pb.Suppressions, pbRule)
	}
	return pb
}

func (s *Server) ListSecurityPolicyPacks(ctx context.Context, req *platform.ListSecurityPolicyPacksRequest) (*platform.ListSecurityPolicyPacksResponse, error) {
	list := &triggersv1alpha1.SecurityPolicyPackList{}
	usage, err := s.listSecurityLibraryResources(ctx, req.GetNamespace(), "SecurityPolicyPack", "SecurityPolicyPacks", list)
	if err != nil {
		return nil, err
	}
	resp := &platform.ListSecurityPolicyPacksResponse{}
	for i := range list.Items {
		cr := &list.Items[i]
		resp.PolicyPacks = append(resp.PolicyPacks, securityPolicyPackToProto(cr, usage[cr.Name]))
	}
	sort.Slice(resp.PolicyPacks, func(i, j int) bool { return resp.PolicyPacks[i].Name < resp.PolicyPacks[j].Name })
	return resp, nil
}

func (s *Server) GetSecurityPolicyPack(ctx context.Context, req *platform.GetSecurityPolicyPackRequest) (*platform.SecurityPolicyPackResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := validateResourceName(req.GetName()); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityPolicyPack{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: req.GetName()}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityPolicyPack %s/%s", namespace, req.GetName()), err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityPolicyPack")
	if err != nil {
		return nil, err
	}
	return securityPolicyPackToProto(cr, usage[cr.Name]), nil
}

func (s *Server) CreateSecurityPolicyPack(ctx context.Context, req *platform.CreateSecurityPolicyPackRequest) (*platform.SecurityPolicyPackResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetPolicyPack().GetNamespace())
	if err != nil {
		return nil, err
	}
	spec, err := securityPolicyPackSpecFromProto(req.GetPolicyPack())
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityPolicyPack{
		ObjectMeta: metav1.ObjectMeta{Name: req.GetPolicyPack().GetName(), Namespace: namespace},
		Spec:       spec,
	}
	if err := s.k8sClient.Create(ctx, cr); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("SecurityPolicyPack %s/%s already exists", namespace, cr.Name))
		}
		return nil, mapK8sError("create SecurityPolicyPack", err)
	}
	return securityPolicyPackToProto(cr, nil), nil
}

func (s *Server) UpdateSecurityPolicyPack(ctx context.Context, req *platform.UpdateSecurityPolicyPackRequest) (*platform.SecurityPolicyPackResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetPolicyPack().GetNamespace())
	if err != nil {
		return nil, err
	}
	spec, err := securityPolicyPackSpecFromProto(req.GetPolicyPack())
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityPolicyPack{}
	usage, err := s.updateSecurityLibraryResource(ctx, namespace, req.GetPolicyPack().GetName(), "SecurityPolicyPack", cr, func() { cr.Spec = spec })
	if err != nil {
		return nil, err
	}
	return securityPolicyPackToProto(cr, usage), nil
}

func (s *Server) DeleteSecurityPolicyPack(ctx context.Context, req *platform.DeleteSecurityPolicyPackRequest) (*emptypb.Empty, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := validateResourceName(req.GetName()); err != nil {
		return nil, err
	}
	if err := s.guardSecurityLibraryDelete(ctx, namespace, "SecurityPolicyPack", req.GetName()); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityPolicyPack{ObjectMeta: metav1.ObjectMeta{Name: req.GetName(), Namespace: namespace}}
	if err := s.k8sClient.Delete(ctx, cr); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete SecurityPolicyPack", err)
	}
	return &emptypb.Empty{}, nil
}
