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

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// The events.k8s.io recorder folds an event into an earlier one on the same
// object, the object's resourceVersion included, with the same type, reason,
// action and related object, and keeps the earlier note: kubectl describe
// shows the old note, counted twice. A fake recorder never folds (P85). Each
// case sends, through the recorder the operator runs, a second event that
// says something else, on an object that hasn't changed, and reads what the
// API server kept. The same warning again is still counted twice, as it
// should be.
func TestEvents_ANewerNoteIsntFoldedIntoAnOlderOne(t *testing.T) {
	cfg := startEnvtest(t)
	scheme := testScheme(t)
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	namespace := func(t *testing.T, name string, labels, annotations map[string]string) *corev1.Namespace {
		t.Helper()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, Annotations: annotations}}
		require.NoError(t, admin.Create(ctx, ns))
		return ns
	}
	optIn := func(sent sentEvents) *OptInReconciler {
		return &OptInReconciler{Client: admin, Scheme: scheme, Recorder: sent.recorder,
			Kind: v1alpha1.TargetKindDeployment, Defaults: DefaultOptInDefaults}
	}
	reconcileAPI := func(t *testing.T, r *OptInReconciler, ns string) {
		t.Helper()
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Namespace: ns, Name: "api"}})
		require.NoError(t, err)
	}
	update := func(t *testing.T, obj client.Object, change func()) {
		t.Helper()
		require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(obj), obj))
		change()
		require.NoError(t, admin.Update(ctx, obj))
	}

	t.Run("two settings it can't read, then one on the namespace", func(t *testing.T) {
		ns := namespace(t, "settings", nil, nil)
		d := envtestDeployment(ns.Name, 1)
		d.Labels = map[string]string{v1alpha1.LabelManaged: v1alpha1.True}
		d.Annotations = map[string]string{v1alpha1.AnnotationIdleAfter: "soon", v1alpha1.AnnotationCPUThreshold: "lots"}
		require.NoError(t, admin.Create(ctx, d))
		sent := startEventRecorder(t, cfg, scheme)
		r := optIn(sent)

		reconcileAPI(t, r, ns.Name)
		sent.shows(t, ns.Name, "Deployment", "api", ReasonInvalidSetting, `napwake.dev/idle-after="soon" on the workload`)
		sent.shows(t, ns.Name, "Deployment", "api", ReasonInvalidSetting, `napwake.dev/cpu-threshold="lots" on the workload`)
		reconcileAPI(t, r, ns.Name)
		sent.shows(t, ns.Name, "Deployment", "api", ReasonInvalidSetting, `x2 napwake.dev/idle-after="soon"`)

		update(t, ns, func() { ns.Annotations = map[string]string{v1alpha1.AnnotationWakeMaxWait: "forever"} })
		reconcileAPI(t, r, ns.Name)
		sent.shows(t, ns.Name, "Deployment", "api", ReasonInvalidSetting,
			`napwake.dev/wake-max-wait="forever" on the namespace`)
	})

	t.Run("a label on the pod template, then names taken once its namespace is labelled", func(t *testing.T) {
		ns := namespace(t, "names", nil, nil)
		for _, mw := range []*v1alpha1.ManagedWorkload{
			{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns.Name},
				Spec: v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindStatefulSet, Name: "api"}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "api-deployment", Namespace: ns.Name},
				Spec: v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "other"}}},
		} {
			require.NoError(t, admin.Create(ctx, mw))
		}
		d := envtestDeployment(ns.Name, 1)
		d.Spec.Template.Labels[v1alpha1.LabelManaged] = v1alpha1.True
		require.NoError(t, admin.Create(ctx, d))
		sent := startEventRecorder(t, cfg, scheme)
		r := optIn(sent)

		reconcileAPI(t, r, ns.Name)
		sent.shows(t, ns.Name, "Deployment", "api", ReasonLabelIgnored, "is on the pod template")

		update(t, ns, func() { ns.Labels = map[string]string{v1alpha1.LabelManaged: v1alpha1.True} })
		reconcileAPI(t, r, ns.Name)
		sent.shows(t, ns.Name, "Deployment", "api", ReasonLabelIgnored,
			"ManagedWorkloads named api and api-deployment already exist")
	})

	t.Run("let go of when its namespace's label goes, then when one is written for it", func(t *testing.T) {
		ns := namespace(t, "letgo", map[string]string{v1alpha1.LabelManaged: v1alpha1.True}, nil)
		require.NoError(t, admin.Create(ctx, envtestDeployment(ns.Name, 1)))
		sent := startEventRecorder(t, cfg, scheme)
		r := optIn(sent)

		reconcileAPI(t, r, ns.Name)
		update(t, ns, func() { ns.Labels = nil })
		reconcileAPI(t, r, ns.Name)
		sent.shows(t, ns.Name, "Deployment", "api", ReasonNoLongerManaged, "the managed label was removed")

		update(t, ns, func() { ns.Labels = map[string]string{v1alpha1.LabelManaged: v1alpha1.True} })
		reconcileAPI(t, r, ns.Name)
		require.NoError(t, admin.Create(ctx, &v1alpha1.ManagedWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: "api-by-hand", Namespace: ns.Name},
			Spec:       v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"}}}))
		reconcileAPI(t, r, ns.Name)
		sent.shows(t, ns.Name, "Deployment", "api", ReasonNoLongerManaged, "ManagedWorkload api-by-hand manages it")
	})

	t.Run("two activity annotations it can't read, then a new value on the workload", func(t *testing.T) {
		ns := namespace(t, "activity", nil, nil)
		target := envtestDeployment(ns.Name, 1)
		target.Annotations = map[string]string{v1alpha1.AnnotationActiveUntil: "tomorrow"}
		require.NoError(t, admin.Create(ctx, target))
		mw := envtestWorkload(ns.Name)
		mw.Annotations = map[string]string{v1alpha1.AnnotationLastActivity: "1762588800"}
		require.NoError(t, createTakenOn(ctx, admin, mw))
		sent := startEventRecorder(t, cfg, scheme)
		r := &Reconciler{Client: admin, Scheme: scheme, Recorder: sent.recorder, clock: time.Now}

		r.warnMalformedActivity(mw, target)
		sent.shows(t, ns.Name, "ManagedWorkload", "api", ReasonInvalidActivityAnnotation, `on ManagedWorkload api is "1762588800"`)
		sent.shows(t, ns.Name, "ManagedWorkload", "api", ReasonInvalidActivityAnnotation, `on Deployment api is "tomorrow"`)

		update(t, target, func() { target.Annotations[v1alpha1.AnnotationActiveUntil] = "next week" })
		r.warnMalformedActivity(mw, target)
		sent.shows(t, ns.Name, "ManagedWorkload", "api", ReasonInvalidActivityAnnotation, `on Deployment api is "next week"`)
	})

	t.Run("two dependencies it can't wake", func(t *testing.T) {
		ns := namespace(t, "wakes", nil, nil)
		for _, name := range []string{"api", "db"} {
			require.NoError(t, createTakenOn(ctx, admin, &v1alpha1.ManagedWorkload{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name},
				Spec:       v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: name}}}))
		}
		web := &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns.Name},
			Spec: v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "web"},
				DependsOn: []v1alpha1.DependencyRef{{Kind: v1alpha1.TargetKindDeployment, Name: "api"},
					{Kind: v1alpha1.TargetKindDeployment, Name: "db"}}}}
		require.NoError(t, createTakenOn(ctx, admin, web))
		denied := interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch,
			...client.PatchOption) error {
			return errors.New("admission webhook denied the request")
		}}
		watching, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
		require.NoError(t, err)
		sent := startEventRecorder(t, cfg, scheme)
		r := &Reconciler{Client: interceptor.NewClient(watching, denied), Scheme: scheme, Recorder: sent.recorder,
			clock: time.Now}

		r.wakeDependencies(ctx, web)
		sent.shows(t, ns.Name, "ManagedWorkload", "web", ReasonDependencyWakeFailed, "couldn't wake wakes/api")
		sent.shows(t, ns.Name, "ManagedWorkload", "web", ReasonDependencyWakeFailed, "couldn't wake wakes/db")
		r.wakeDependencies(ctx, web)
		sent.shows(t, ns.Name, "ManagedWorkload", "web", ReasonDependencyWakeFailed, "x2 web: couldn't wake wakes/db")
	})

	t.Run("a pause record it can't read, then another", func(t *testing.T) {
		ns := namespace(t, "record", nil, nil)
		target := envtestDeployment(ns.Name, 1)
		target.Annotations = map[string]string{v1alpha1.AnnotationPausedFrom: "not json"}
		require.NoError(t, admin.Create(ctx, target))
		mw := envtestWorkload(ns.Name)
		require.NoError(t, createTakenOn(ctx, admin, mw))
		sent := startEventRecorder(t, cfg, scheme)
		r := &Reconciler{Client: admin, PodReader: admin, Scheme: scheme, Recorder: sent.recorder, clock: time.Now}

		_, err := r.pauseRecordOn(ctx, mw, target)
		require.NoError(t, err)
		sent.shows(t, ns.Name, "ManagedWorkload", "api", ReasonPauseRecordLost, "invalid character")

		update(t, target, func() { target.Annotations[v1alpha1.AnnotationPausedFrom] = `{"replicas":"two"}` })
		_, err = r.pauseRecordOn(ctx, mw, target)
		require.NoError(t, err)
		sent.shows(t, ns.Name, "ManagedWorkload", "api", ReasonPauseRecordLost, "cannot unmarshal string")
	})
}

// shows waits for the API server to keep an event of reason on the named
// object whose note says want, as kubectl describe would show it, and
// fails with what it keeps instead.
func (s sentEvents) shows(t *testing.T, namespace, kind, name, reason, want string) {
	t.Helper()
	notes := func() []string {
		var out []string
		for _, e := range s.list(t, namespace) {
			if e.Regarding.Kind != kind || e.Regarding.Name != name || e.Reason != reason {
				continue
			}
			count := int32(1)
			if e.Series != nil {
				count = e.Series.Count
			}
			out = append(out, fmt.Sprintf("x%d %s", count, e.Note))
		}
		slices.Sort(out)
		return out
	}
	if !assert.Eventually(t, func() bool {
		return slices.ContainsFunc(notes(), func(n string) bool { return strings.Contains(n, want) })
	}, 10*time.Second, 50*time.Millisecond) {
		t.Fatalf("kubectl describe %s %s would show its %s events as %q, none saying %q",
			strings.ToLower(kind), name, reason, notes(), want)
	}
}
