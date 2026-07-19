package main

import (
	"context"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func guardrailTestClient(t *testing.T, policies ...*platformv1alpha1.GuardrailPolicy) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	objects := make([]client.Object, len(policies))
	for i, policy := range policies {
		objects[i] = policy
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func TestLoadCRDGuardrailsValidPolicy(t *testing.T) {
	t.Parallel()
	policy := &platformv1alpha1.GuardrailPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: platformv1alpha1.GuardrailPolicySpec{Rules: []platformv1alpha1.GuardrailRule{
			{Name: "input", Type: "tool-input", Regex: "danger", ToolPattern: "bash*", Action: "block"},
			{Name: "output", Type: "tool-output", Regex: "credential", ToolPattern: "*", Action: "warn"},
		}},
	}

	input, output, err := loadCRDGuardrails(context.Background(), guardrailTestClient(t, policy), &platformv1alpha1.NamedRef{Name: "policy"}, "default")
	if err != nil {
		t.Fatalf("loadCRDGuardrails() error = %v", err)
	}
	if len(input) != 1 || input[0].Name != "crd:input" {
		t.Errorf("input guardrails = %v, want one named crd:input", input)
	}
	if len(output) != 1 || output[0].Name != "crd:output" {
		t.Errorf("output guardrails = %v, want one named crd:output", output)
	}
}

func TestLoadCRDGuardrailsMissingPolicyFails(t *testing.T) {
	t.Parallel()
	input, output, err := loadCRDGuardrails(context.Background(), guardrailTestClient(t), &platformv1alpha1.NamedRef{Name: "missing"}, "default")
	if err == nil {
		t.Fatal("loadCRDGuardrails() error = nil, want NotFound error")
	}
	if input != nil || output != nil {
		t.Fatalf("guardrails = (%v, %v), want no partial rules", input, output)
	}
}

func TestLoadCRDGuardrailsReferencedPolicyRequiresClientAndName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		c    client.Client
		ref  *platformv1alpha1.NamedRef
	}{
		{name: "nil client", ref: &platformv1alpha1.NamedRef{Name: "policy"}},
		{name: "empty reference name", c: guardrailTestClient(t), ref: &platformv1alpha1.NamedRef{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input, output, err := loadCRDGuardrails(context.Background(), tt.c, tt.ref, "default")
			if err == nil {
				t.Fatal("loadCRDGuardrails() error = nil, want error")
			}
			if input != nil || output != nil {
				t.Fatalf("guardrails = (%v, %v), want no partial rules", input, output)
			}
		})
	}
}

func TestLoadCRDGuardrailsConversionErrorDiscardsPartialRules(t *testing.T) {
	t.Parallel()
	policy := &platformv1alpha1.GuardrailPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid", Namespace: "default"},
		Spec: platformv1alpha1.GuardrailPolicySpec{Rules: []platformv1alpha1.GuardrailRule{
			{Name: "valid", Type: "tool-input", Regex: "safe", Action: "block"},
			{Name: "invalid", Type: "tool-output", Regex: "[", Action: "block"},
		}},
	}

	input, output, err := loadCRDGuardrails(context.Background(), guardrailTestClient(t, policy), &platformv1alpha1.NamedRef{Name: "invalid"}, "default")
	if err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("loadCRDGuardrails() error = %v, want invalid regex error", err)
	}
	if input != nil || output != nil {
		t.Fatalf("guardrails = (%v, %v), want no partial rules", input, output)
	}
}
