package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func securityProgramSpecFromProto(pb *platform.SecurityProgramResource) (triggersv1alpha1.SecurityProgramSpec, error) {
	if pb == nil {
		return triggersv1alpha1.SecurityProgramSpec{}, invalidArgument("security program is required")
	}
	if err := validateResourceName(pb.GetName()); err != nil {
		return triggersv1alpha1.SecurityProgramSpec{}, err
	}
	var verifiedAt metav1.Time
	if timestamp := pb.GetVerifiedAt(); timestamp != nil {
		if err := timestamp.CheckValid(); err != nil {
			return triggersv1alpha1.SecurityProgramSpec{}, invalidArgument("verified_at is invalid: %v", err)
		}
		verifiedAt = metav1.NewTime(timestamp.AsTime())
	}
	spec := triggersv1alpha1.SecurityProgramSpec{
		Provider:    strings.TrimSpace(pb.GetProvider()),
		DisplayName: strings.TrimSpace(pb.GetDisplayName()),
		ProgramURL:  strings.TrimSpace(pb.GetProgramUrl()),
		ScopePolicy: pb.GetScopePolicy(),
		VerifiedAt:  verifiedAt,
	}
	if errs := triggersv1alpha1.ValidateSecurityProgramSpec(spec); len(errs) != 0 {
		return triggersv1alpha1.SecurityProgramSpec{}, securityLibraryInvalidArgument(errs)
	}
	return spec, nil
}

func securityProgramContentDigest(spec triggersv1alpha1.SecurityProgramSpec) string {
	contents, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func securityProgramToProto(cr *triggersv1alpha1.SecurityProgram, referencing []string) *platform.SecurityProgramResource {
	pb := &platform.SecurityProgramResource{
		Namespace:        cr.Namespace,
		Name:             cr.Name,
		Provider:         cr.Spec.Provider,
		DisplayName:      cr.Spec.DisplayName,
		ProgramUrl:       cr.Spec.ProgramURL,
		ScopePolicy:      cr.Spec.ScopePolicy,
		VerifiedAt:       timestamppb.New(cr.Spec.VerifiedAt.Time),
		UsageCount:       int32(len(referencing)), //nolint:gosec // scan counts stay far below int32 bounds
		ReferencingScans: referencing,
		Generation:       cr.Generation,
		CreatedAtUnix:    cr.CreationTimestamp.Unix(),
	}
	if ready := meta.FindStatusCondition(cr.Status.Conditions, triggersv1alpha1.ConditionSecurityLibraryReady); ready != nil {
		currentDigest := securityProgramContentDigest(cr.Spec)
		if currentDigest != "" && cr.Status.ObservedGeneration == cr.Generation &&
			ready.ObservedGeneration == cr.Generation &&
			cr.Status.ContentDigest != "" && cr.Status.ContentDigest == currentDigest {
			pb.ConditionReady = string(ready.Status)
			pb.ContentDigest = cr.Status.ContentDigest
		}
	}
	return pb
}

func (s *Server) ListSecurityPrograms(ctx context.Context, req *platform.ListSecurityProgramsRequest) (*platform.ListSecurityProgramsResponse, error) {
	list := &triggersv1alpha1.SecurityProgramList{}
	usage, err := s.listSecurityLibraryResources(ctx, req.GetNamespace(), "SecurityProgram", "SecurityPrograms", list)
	if err != nil {
		return nil, err
	}
	resp := &platform.ListSecurityProgramsResponse{}
	for i := range list.Items {
		cr := &list.Items[i]
		resp.Programs = append(resp.Programs, securityProgramToProto(cr, usage[cr.Name]))
	}
	sort.Slice(resp.Programs, func(i, j int) bool { return resp.Programs[i].Name < resp.Programs[j].Name })
	return resp, nil
}

func (s *Server) GetSecurityProgram(ctx context.Context, req *platform.GetSecurityProgramRequest) (*platform.SecurityProgramResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := validateResourceName(req.GetName()); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityProgram{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: req.GetName()}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityProgram %s/%s", namespace, req.GetName()), err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityProgram")
	if err != nil {
		return nil, err
	}
	return securityProgramToProto(cr, usage[cr.Name]), nil
}

func (s *Server) CreateSecurityProgram(ctx context.Context, req *platform.CreateSecurityProgramRequest) (*platform.SecurityProgramResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetProgram().GetNamespace())
	if err != nil {
		return nil, err
	}
	spec, err := securityProgramSpecFromProto(req.GetProgram())
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityProgram{
		ObjectMeta: metav1.ObjectMeta{Name: req.GetProgram().GetName(), Namespace: namespace},
		Spec:       spec,
	}
	if err := s.k8sClient.Create(ctx, cr); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("SecurityProgram %s/%s already exists", namespace, cr.Name))
		}
		return nil, mapK8sError("create SecurityProgram", err)
	}
	return securityProgramToProto(cr, nil), nil
}

func (s *Server) UpdateSecurityProgram(ctx context.Context, req *platform.UpdateSecurityProgramRequest) (*platform.SecurityProgramResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetProgram().GetNamespace())
	if err != nil {
		return nil, err
	}
	spec, err := securityProgramSpecFromProto(req.GetProgram())
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityProgram{}
	usage, err := s.updateSecurityLibraryResource(ctx, namespace, req.GetProgram().GetName(), "SecurityProgram", cr, func() { cr.Spec = spec })
	if err != nil {
		return nil, err
	}
	return securityProgramToProto(cr, usage), nil
}

func (s *Server) DeleteSecurityProgram(ctx context.Context, req *platform.DeleteSecurityProgramRequest) (*emptypb.Empty, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := validateResourceName(req.GetName()); err != nil {
		return nil, err
	}
	if err := s.guardSecurityLibraryDelete(ctx, namespace, "SecurityProgram", req.GetName()); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityProgram{ObjectMeta: metav1.ObjectMeta{Name: req.GetName(), Namespace: namespace}}
	if err := s.k8sClient.Delete(ctx, cr); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete SecurityProgram", err)
	}
	return &emptypb.Empty{}, nil
}
