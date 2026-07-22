package dashboard

import (
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGitHubRepositoryMaintainerAdapterRejectsInvalidCutover(t *testing.T) {
	enabled := true
	invalid := "Unsafe"
	_, _, _, _, _, _, _, err := protoGitHubTriggerSettingsToCRD(&platform.GitHubRepositoryTriggerSettings{
		MaintainerEnabled:         &enabled,
		MaintainerWorkItemCutover: &invalid,
	})
	if err == nil {
		t.Fatal("expected invalid maintainer work-item cutover to be rejected")
	}
}

func TestGitHubRepositoryMaintainerAdapterRoundTrip(t *testing.T) {
	lastWake := metav1.NewTime(time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC))
	lastReport := metav1.NewTime(time.Date(2026, 3, 7, 13, 0, 0, 0, time.UTC))
	gh := &triggersv1alpha1.GitHubRepository{
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Maintainer: &triggersv1alpha1.MaintainerSpec{
				ModeRef:                 &platformv1alpha1.ModeRef{Name: "repository-maintainer"},
				Model:                   "claude-opus-4-6",
				MaxConcurrentDispatches: 4,
				MaxDispatchesPerDay:     12,
				StandupInterval:         &metav1.Duration{Duration: 6 * time.Hour},
				AllowPullRequestMerge:   true,
				WorkItemCutover:         triggersv1alpha1.MaintainerWorkItemCutoverDualRead,
			},
		},
		Status: triggersv1alpha1.GitHubRepositoryStatus{
			Maintainer: &triggersv1alpha1.MaintainerStatus{
				RunName:           "acme-payments-maintainer",
				LastWakeTime:      &lastWake,
				DispatchesToday:   3,
				LastReportTime:    &lastReport,
				LastReportState:   triggersv1alpha1.MaintainerReportStateAttention,
				LastReportSummary: "Two issues need triage.",
			},
		},
	}

	out := k8sGitHubRepositoryToProto(gh)
	settings := out.GetTriggerSettings()
	if settings == nil || !settings.GetMaintainerEnabled() ||
		settings.GetMaintainerMaxConcurrentDispatches() != 4 || settings.GetMaintainerMaxDispatchesPerDay() != 12 ||
		settings.GetMaintainerStandupInterval() != "6h0m0s" || settings.GetMaintainerModeRef() != "repository-maintainer" ||
		settings.GetMaintainerModel() != "claude-opus-4-6" || !settings.GetMaintainerAllowPrMerge() ||
		settings.GetMaintainerWorkItemCutover() != string(triggersv1alpha1.MaintainerWorkItemCutoverDualRead) {
		t.Fatalf("maintainer settings = %+v", settings)
	}
	if out.MaintainerStatus == nil || out.MaintainerStatus.RunName != "acme-payments-maintainer" ||
		out.MaintainerStatus.LastWakeUnix != lastWake.Unix() || out.MaintainerStatus.DispatchesToday != 3 ||
		out.MaintainerStatus.LastReportTimeUnix != lastReport.Unix() || out.MaintainerStatus.LastReportState != triggersv1alpha1.MaintainerReportStateAttention ||
		out.MaintainerStatus.LastReportSummary != "Two issues need triage." {
		t.Fatalf("maintainer status = %+v", out.MaintainerStatus)
	}

	_, _, _, _, _, _, maintainer, err := protoGitHubTriggerSettingsToCRD(settings)
	if err != nil {
		t.Fatalf("protoGitHubTriggerSettingsToCRD() error = %v", err)
	}
	if maintainer == nil || maintainer.Disabled || maintainer.MaxConcurrentDispatches != 4 ||
		maintainer.MaxDispatchesPerDay != 12 || maintainer.StandupInterval == nil || maintainer.StandupInterval.Duration != 6*time.Hour ||
		maintainer.ModeRef == nil || maintainer.ModeRef.Name != "repository-maintainer" || maintainer.Model != "claude-opus-4-6" ||
		!maintainer.AllowPullRequestMerge || maintainer.WorkItemCutover != triggersv1alpha1.MaintainerWorkItemCutoverDualRead {
		t.Fatalf("round-tripped maintainer = %+v", maintainer)
	}
}
