/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CronSpec defines the desired state of Cron.
type CronSpec struct {
	// schedule is a standard 5-field cron expression, or a descriptor supported
	// by robfig/cron such as "@hourly".
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// timeZone is the IANA time zone used to evaluate schedule.
	// When omitted, UTC is used.
	// +kubebuilder:default="UTC"
	// +optional
	TimeZone string `json:"timeZone,omitempty"`

	// suspend pauses new AgentRun creation while keeping status readable.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// concurrencyPolicy controls whether a scheduled tick may create a new
	// AgentRun while a previous AgentRun from this Cron is still active.
	// Empty defaults to Forbid for safety.
	// +kubebuilder:validation:Enum=Allow;Forbid
	// +optional
	ConcurrencyPolicy CronConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// prompt is seeded as the first user message for every scheduled AgentRun.
	// +kubebuilder:validation:MinLength=1
	Prompt string `json:"prompt"`

	// defaults holds the fields used when creating AgentRuns.
	Defaults AgentRunDefaults `json:"defaults"`
}

// CronStatus defines the observed state of Cron.
type CronStatus struct {
	// lastScheduleTime is the schedule instant most recently processed.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// nextScheduleTime is the next schedule instant the controller expects to process.
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`

	// lastRunName is the AgentRun name created for lastScheduleTime.
	// +optional
	LastRunName string `json:"lastRunName,omitempty"`

	// runsCreated is the cumulative number of AgentRuns created by this trigger.
	// +optional
	RunsCreated int32 `json:"runsCreated,omitempty"`

	// observedSchedule is the schedule string last accepted by the controller.
	// +optional
	ObservedSchedule string `json:"observedSchedule,omitempty"`

	// observedTimeZone is the time zone last accepted by the controller.
	// +optional
	ObservedTimeZone string `json:"observedTimeZone,omitempty"`

	// lastError contains the error message from the most recent failed operation.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// conditions represent the current state of the Cron trigger.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for Cron.
const (
	ConditionCronReady = "Ready"
)

// CronConcurrencyPolicy controls overlapping scheduled AgentRuns.
type CronConcurrencyPolicy string

const (
	CronConcurrencyAllow  CronConcurrencyPolicy = "Allow"
	CronConcurrencyForbid CronConcurrencyPolicy = "Forbid"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="Runs",type=integer,JSONPath=`.status.runsCreated`
// +kubebuilder:printcolumn:name="LastSchedule",type=date,JSONPath=`.status.lastScheduleTime`
// +kubebuilder:printcolumn:name="NextSchedule",type=date,JSONPath=`.status.nextScheduleTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Cron is the Schema for the crons API.
type Cron struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec CronSpec `json:"spec"`

	// +optional
	Status CronStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CronList contains a list of Cron.
type CronList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Cron `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Cron{}, &CronList{})
}
