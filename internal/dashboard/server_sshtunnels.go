/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package dashboard

import (
	"context"
	"sort"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	platform "github.com/gratefulagents/gratefulagents/rpc/platform"
)

// ListSSHTunnels lists the SSHTunnel resources in the caller's namespace so
// run-defaults forms can pick a tunnel by name and show its health.
// SSHTunnels are kubectl/GitOps-authored (their Secrets carry SSH private
// keys, so the dashboard intentionally offers no create/update path); like
// other unowned resources they stay visible to everyone in the namespace.
func (s *Server) ListSSHTunnels(ctx context.Context, _ *platform.ListSSHTunnelsRequest) (*platform.ListSSHTunnelsResponse, error) {
	ns, err := dashboardNamespace(ctx, s)
	if err != nil {
		return nil, err
	}
	var list platformv1alpha1.SSHTunnelList
	if err := s.k8sClient.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, mapK8sError("list SSHTunnels", err)
	}
	out := &platform.ListSSHTunnelsResponse{Namespace: ns}
	for i := range list.Items {
		out.Tunnels = append(out.Tunnels, sshTunnelToProto(&list.Items[i]))
	}
	sort.Slice(out.Tunnels, func(i, j int) bool { return out.Tunnels[i].Name < out.Tunnels[j].Name })
	return out, nil
}

// sshTunnelToProto maps the CRD to its read-only dashboard summary. Secret
// references and contents are deliberately omitted.
func sshTunnelToProto(t *platformv1alpha1.SSHTunnel) *platform.SSHTunnel {
	pb := &platform.SSHTunnel{
		Namespace:     t.Namespace,
		Name:          t.Name,
		Host:          t.Spec.Host,
		Port:          t.Spec.Port,
		User:          t.Spec.User,
		RemoteHost:    t.Spec.RemoteHost,
		RemotePort:    t.Spec.RemotePort,
		Description:   t.Spec.Description,
		Phase:         t.Status.Phase,
		CreatedAtUnix: t.CreationTimestamp.Unix(),
	}
	if cond := meta.FindStatusCondition(t.Status.Conditions, "Ready"); cond != nil {
		pb.Message = cond.Message
	}
	return pb
}
