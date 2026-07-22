package dashboard

import (
	"context"
	"fmt"
	"sort"

	"connectrpc.com/connect"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// maintainerWorkItemPhaseOrder sorts active work ahead of terminal work so the
// dashboard queue reads top-down as "what the maintainer is doing now".
var maintainerWorkItemPhaseOrder = map[triggersv1alpha1.MaintainerWorkItemPhase]int{
	triggersv1alpha1.MaintainerWorkItemPhaseAwaitingDecision: 0,
	triggersv1alpha1.MaintainerWorkItemPhaseReadyToMerge:     1,
	triggersv1alpha1.MaintainerWorkItemPhaseImplementing:     2,
	triggersv1alpha1.MaintainerWorkItemPhaseDispatched:       3,
	triggersv1alpha1.MaintainerWorkItemPhaseReadyToDispatch:  4,
	triggersv1alpha1.MaintainerWorkItemPhaseTriaged:          5,
	triggersv1alpha1.MaintainerWorkItemPhasePendingTriage:    6,
	triggersv1alpha1.MaintainerWorkItemPhaseDelivered:        7,
}

func maintainerWorkItemPhaseRank(phase string) int {
	if order, ok := maintainerWorkItemPhaseOrder[triggersv1alpha1.MaintainerWorkItemPhase(phase)]; ok {
		return order
	}
	// Unknown phases (including empty) group with pending triage.
	return maintainerWorkItemPhaseOrder[triggersv1alpha1.MaintainerWorkItemPhasePendingTriage]
}

// maintainerWorkItemRank orders the dashboard queue. NotActionable items are
// terminal (the controller closes the issue but leaves the phase Triaged), so
// they sink below Delivered work instead of ranking as active triage.
func maintainerWorkItemRank(item *platform.MaintainerWorkItem) int {
	if item.Disposition == string(triggersv1alpha1.MaintainerWorkItemDispositionNotActionable) {
		return len(maintainerWorkItemPhaseOrder)
	}
	return maintainerWorkItemPhaseRank(item.Phase)
}

// ListMaintainerWorkItems returns the durable maintainer work items for one
// GitHubRepository trigger, gated on viewer access to that repository.
func (s *Server) ListMaintainerWorkItems(ctx context.Context, req *platform.ListMaintainerWorkItemsRequest) (*platform.ListMaintainerWorkItemsResponse, error) {
	if req.GetNamespace() == "" || req.GetRepositoryName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and repository_name are required"))
	}
	if err := s.requireResourceAccess(ctx, githubRepositoryResourceType, req.RepositoryName, req.Namespace, AccessViewer, "view this repository"); err != nil {
		return nil, err
	}
	items := &triggersv1alpha1.MaintainerWorkItemList{}
	if err := s.k8sClient.List(ctx, items, client.InNamespace(req.Namespace)); err != nil {
		return nil, mapK8sError(fmt.Sprintf("list MaintainerWorkItems in %s", req.Namespace), err)
	}

	var pbItems []*platform.MaintainerWorkItem
	for i := range items.Items {
		item := &items.Items[i]
		if item.Spec.RepositoryRef.Name != req.RepositoryName {
			continue
		}
		pbItems = append(pbItems, maintainerWorkItemToProto(item))
	}
	sort.SliceStable(pbItems, func(a, b int) bool {
		orderA := maintainerWorkItemRank(pbItems[a])
		orderB := maintainerWorkItemRank(pbItems[b])
		if orderA != orderB {
			return orderA < orderB
		}
		return pbItems[a].IssueNumber > pbItems[b].IssueNumber
	})
	return &platform.ListMaintainerWorkItemsResponse{Items: pbItems}, nil
}

// maintainerWorkItemToProto projects the MaintainerWorkItem CRD into the
// dashboard read model.
func maintainerWorkItemToProto(item *triggersv1alpha1.MaintainerWorkItem) *platform.MaintainerWorkItem {
	pb := &platform.MaintainerWorkItem{
		Namespace:       item.Namespace,
		Name:            item.Name,
		RepositoryName:  item.Spec.RepositoryRef.Name,
		IssueNumber:     item.Spec.IssueNumber,
		Disposition:     string(item.Spec.Disposition),
		Phase:           string(item.Status.Phase),
		EvidenceSummary: item.Spec.EvidenceSummary,
		CreatedAtUnix:   item.CreationTimestamp.Unix(),
	}
	if pb.Phase == "" {
		pb.Phase = string(triggersv1alpha1.MaintainerWorkItemPhasePendingTriage)
	}
	if item.Spec.CloseReason != nil {
		pb.CloseReason = string(*item.Spec.CloseReason)
	}
	if issue := item.Status.IssueObservation; issue != nil {
		pb.IssueTitle = issue.Title
		pb.IssueUrl = issue.URL
		pb.IssueState = string(issue.State)
	}
	if readiness := item.Status.Readiness; readiness != nil {
		pb.ReadyToDispatch = readiness.ReadyToDispatch
		pb.ReadyToMerge = readiness.ReadyToMerge
		pb.UnmetRequirements = append(pb.UnmetRequirements, readiness.UnmetRequirements...)
	}
	if decision := item.Status.PendingDecision; decision != nil {
		pb.PendingDecision = &platform.MaintainerWorkItemDecision{
			Id:              decision.ID,
			Question:        decision.Question,
			Options:         append([]string(nil), decision.Options...),
			RequestedAtUnix: decision.RequestedAt.Unix(),
		}
	}
	for _, run := range item.Status.AgentRuns {
		pb.AgentRuns = append(pb.AgentRuns, &platform.MaintainerWorkItemAgentRun{
			Name:        run.Name,
			Role:        string(run.Role),
			Phase:       run.Phase,
			PrLoopState: run.PRLoopState,
		})
	}
	for _, pr := range item.Status.PullRequests {
		pb.PullRequests = append(pb.PullRequests, &platform.MaintainerWorkItemPullRequest{
			Repository:     pr.Repository,
			Number:         pr.Number,
			Url:            pr.URL,
			State:          string(pr.State),
			CheckState:     string(pr.CheckState),
			ReviewDecision: pr.ReviewDecision,
			Draft:          pr.Draft,
		})
	}
	if delivery := item.Status.DeliveryAttestation; delivery != nil {
		pb.DeliverySummary = delivery.DeliverySummary
		if delivery.CompletedAt != nil {
			pb.DeliveredAtUnix = delivery.CompletedAt.Unix()
		}
	}
	pb.ChildrenTotal = int32(len(item.Status.Children))
	if pb.ChildrenTotal == 0 {
		pb.ChildrenTotal = int32(len(item.Spec.Children))
	}
	for _, child := range item.Status.Children {
		if child.Delivered {
			pb.ChildrenDelivered++
		}
	}
	pb.DependenciesTotal = int32(len(item.Status.Dependencies))
	if pb.DependenciesTotal == 0 {
		pb.DependenciesTotal = int32(len(item.Spec.Dependencies))
	}
	for _, dep := range item.Status.Dependencies {
		if dep.Delivered {
			pb.DependenciesDelivered++
		}
	}
	if command := item.Status.LatestCommand; command != nil {
		pb.LatestCommandType = string(command.Type)
		pb.LatestCommandPhase = string(command.Phase)
		pb.LatestCommandMessage = command.Message
	}
	return pb
}
