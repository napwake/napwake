/*
Copyright 2026 The Napwake Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gitops

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var t0 = time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)

func entry(manager, fields string, at time.Time) metav1.ManagedFieldsEntry {
	return metav1.ManagedFieldsEntry{Manager: manager, Time: &metav1.Time{Time: at},
		FieldsV1: metav1.NewFieldsV1(fields)}
}

const (
	replicas = `{"f:spec":{"f:replicas":{}}}`
	template = `{"f:spec":{"f:template":{}}}`
)

func TestReplicasWriter(t *testing.T) {
	tests := []struct {
		name   string
		fields []metav1.ManagedFieldsEntry
		want   Writer
		ok     bool
	}{
		{name: "Argo CD", fields: []metav1.ManagedFieldsEntry{entry("argocd-controller", replicas, t0)},
			want: Writer{Manager: "argocd-controller", Tool: ArgoCD, At: t0}, ok: true},
		{name: "older Argo CD", fields: []metav1.ManagedFieldsEntry{entry("argocd-application-controller", replicas, t0)},
			want: Writer{Manager: "argocd-application-controller", Tool: ArgoCD, At: t0}, ok: true},
		{name: "Flux kustomize", fields: []metav1.ManagedFieldsEntry{entry("kustomize-controller", replicas, t0)},
			want: Writer{Manager: "kustomize-controller", Tool: Flux, At: t0}, ok: true},
		{name: "Flux Helm", fields: []metav1.ManagedFieldsEntry{entry("helm-controller", replicas, t0)},
			want: Writer{Manager: "helm-controller", Tool: Flux, At: t0}, ok: true},
		{name: "Rancher Fleet", fields: []metav1.ManagedFieldsEntry{entry("fleetagent", replicas, t0)},
			want: Writer{Manager: "fleetagent", Tool: Fleet, At: t0}, ok: true},
		{name: "kapp", fields: []metav1.ManagedFieldsEntry{entry("kapp", replicas, t0)},
			want: Writer{Manager: "kapp", Tool: Kapp, At: t0}, ok: true},
		{name: "Kluctl", fields: []metav1.ManagedFieldsEntry{entry("kluctl", replicas, t0)},
			want: Writer{Manager: "kluctl", Tool: Kluctl, At: t0}, ok: true},
		{name: "the Kluctl controller", fields: []metav1.ManagedFieldsEntry{entry("kluctl-controller", replicas, t0)},
			want: Writer{Manager: "kluctl-controller", Tool: Kluctl, At: t0}, ok: true},
		{name: "a person", fields: []metav1.ManagedFieldsEntry{entry("kubectl-patch", replicas, t0)},
			want: Writer{Manager: "kubectl-patch", At: t0}, ok: true},
		{name: "the latest of several", fields: []metav1.ManagedFieldsEntry{
			entry("argocd-controller", replicas, t0), entry("kubectl-edit", replicas, t0.Add(time.Hour)),
			entry("kubectl-client-side-apply", replicas, t0.Add(time.Minute))},
			want: Writer{Manager: "kubectl-edit", At: t0.Add(time.Hour)}, ok: true},
		{name: "others' fields don't count", fields: []metav1.ManagedFieldsEntry{
			entry("argocd-controller", template, t0.Add(time.Hour)), entry("kubectl-patch", replicas, t0)},
			want: Writer{Manager: "kubectl-patch", At: t0}, ok: true},
		{name: "a tie goes to the GitOps tool", fields: []metav1.ManagedFieldsEntry{
			entry("kubectl-edit", replicas, t0), entry("argocd-controller", replicas, t0)},
			want: Writer{Manager: "argocd-controller", Tool: ArgoCD, At: t0}, ok: true},
		{name: "a tie goes to the GitOps tool whichever comes first", fields: []metav1.ManagedFieldsEntry{
			entry("kustomize-controller", replicas, t0), entry("kubectl-patch", replicas, t0)},
			want: Writer{Manager: "kustomize-controller", Tool: Flux, At: t0}, ok: true},
		{name: "a tie between people goes to the first", fields: []metav1.ManagedFieldsEntry{
			entry("kubectl-patch", replicas, t0), entry("kubectl-edit", replicas, t0)},
			want: Writer{Manager: "kubectl-patch", At: t0}, ok: true},
		{name: "a GitOps tool after a scale", fields: []metav1.ManagedFieldsEntry{
			entry("argocd-controller", replicas, t0.Add(time.Minute))},
			want: Writer{Manager: "argocd-controller", Tool: ArgoCD, At: t0.Add(time.Minute)}, ok: true},
		{name: "nobody", fields: []metav1.ManagedFieldsEntry{entry("argocd-controller", template, t0)}},
		{name: "no fields", fields: []metav1.ManagedFieldsEntry{{Manager: "argocd-controller"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ReplicasWriter(tt.fields)

			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.want.Tool != "", got.FromGit())
		})
	}
}

func TestFieldWriter(t *testing.T) {
	const (
		dryRun = `{"f:metadata":{"f:annotations":{"f:napwake.dev/dry-run":{}}}}`
		other  = `{"f:metadata":{"f:annotations":{"f:team":{}}}}`
	)
	path := []string{"metadata", "annotations", "napwake.dev/dry-run"}
	tests := []struct {
		name   string
		fields []metav1.ManagedFieldsEntry
		want   Writer
		ok     bool
	}{
		{name: "set from Git", fields: []metav1.ManagedFieldsEntry{entry("kluctl", dryRun, t0)},
			want: Writer{Manager: "kluctl", Tool: Kluctl, At: t0}, ok: true},
		{name: "set by a person after Git", fields: []metav1.ManagedFieldsEntry{
			entry("argocd-controller", dryRun, t0), entry("kubectl-annotate", dryRun, t0.Add(time.Minute))},
			want: Writer{Manager: "kubectl-annotate", At: t0.Add(time.Minute)}, ok: true},
		{name: "only another annotation from Git", fields: []metav1.ManagedFieldsEntry{
			entry("argocd-controller", other, t0.Add(time.Hour)), entry("kubectl-annotate", dryRun, t0)},
			want: Writer{Manager: "kubectl-annotate", At: t0}, ok: true},
		{name: "nobody", fields: []metav1.ManagedFieldsEntry{entry("argocd-controller", other, t0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FieldWriter(tt.fields, path...)

			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFix(t *testing.T) {
	assert.Contains(t, Fix(ArgoCD), "    jsonPointers: [/spec/replicas]")
	for _, tool := range []Tool{Flux, Fleet, Kapp, Kluctl} {
		assert.NotEmpty(t, Fix(tool), "%s has a fix", tool)
	}
	assert.Empty(t, Fix(""))
}

// After pauses in a row undone at once by something other than a GitOps
// tool, Napwake waits an hour after the first, then 2, 4, 8 and 16 hours,
// and at most a day, so an owner that undoes every pause restarts the pods
// a few times a day at most.
func TestUndoneRetryAfter(t *testing.T) {
	for undone, want := range map[int32]time.Duration{
		1: time.Hour, 2: 2 * time.Hour, 3: 4 * time.Hour, 4: 8 * time.Hour, 5: 16 * time.Hour, 6: 24 * time.Hour,
		7: 24 * time.Hour,
	} {
		assert.Equal(t, want, UndoneRetryAfter(undone), "after %d undone", undone)
	}
}

// Across every count a status could hold, the wait is never under the
// hour a GitOps tool gets, never over a day, and never shorter for more.
func TestUndoneRetryAfter_StaysBetweenAnHourAndADay(t *testing.T) {
	last := time.Duration(0)
	check := func(undone int32) {
		got := UndoneRetryAfter(undone)
		if got < RetryAfter || got > 24*time.Hour || got < last {
			t.Fatalf("after %d undone, waits %s, after %s for one fewer", undone, got, last)
		}
		last = got
	}
	for undone := int32(-2); undone <= 1000; undone++ {
		check(undone)
	}
	for _, undone := range []int32{1 << 20, 1<<31 - 2, 1<<31 - 1} {
		check(undone)
	}
	assert.Equal(t, RetryAfter, UndoneRetryAfter(-1<<31), "a count below one is the first")
}
