/*
Copyright 2026.

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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// workloadSetBy is a Deployment whose replicas manager last set, applied by
// Argo CD application shop.
func workloadSetBy(manager string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "orders",
		Annotations:   map[string]string{"argocd.argoproj.io/tracking-id": "shop:apps/Deployment:orders/api"},
		ManagedFields: []metav1.ManagedFieldsEntry{entry(manager, replicas, t0)}}}
}

// application is the Argo CD Application shop, with spec as given.
func application(namespace string, spec map[string]any) *unstructured.Unstructured {
	app := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	app.SetAPIVersion("argoproj.io/v1alpha1")
	app.SetKind("Application")
	app.SetNamespace(namespace)
	app.SetName("shop")
	return app
}

var respecting = map[string]any{"syncOptions": []any{"CreateNamespace=true", "RespectIgnoreDifferences=true"}}

func ignoring(rules ...map[string]any) []any {
	out := make([]any, 0, len(rules))
	for _, r := range rules {
		out = append(out, r)
	}
	return out
}

var replicasOfDeployments = map[string]any{"group": "apps", "kind": "Deployment", "jsonPointers": []any{"/spec/replicas"}}

func argoConfig(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "argocd-cm", Namespace: "argocd"}, Data: data}
}

func checkerOn(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) *Checker {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).WithInterceptorFuncs(funcs).Build()
	return NewChecker(c)
}

// Flux, Fleet and Kluctl set only what the manifest has, so owning the
// replicas means Git sets them; kapp keeps what it's told to as its own, so
// that can't be told.
func TestChecker_ToolsThatDontNeedAsking(t *testing.T) {
	tests := []struct {
		manager string
		want    Undo
	}{
		{manager: "kubectl-edit", want: Leaves},
		{manager: "kustomize-controller", want: Undoes},
		{manager: "helm-controller", want: Undoes},
		{manager: "fleetagent", want: Undoes},
		{manager: "kluctl", want: Undoes},
		{manager: "kapp", want: MayUndo},
	}
	for _, tt := range tests {
		t.Run(tt.manager, func(t *testing.T) {
			got := checkerOn(t, interceptor.Funcs{}).Replicas(context.Background(), workloadSetBy(tt.manager))

			assert.Equal(t, tt.want, got.Undo)
			assert.Equal(t, tt.manager, got.Writer.Manager)
			if tt.want != Leaves {
				assert.Equal(t, Fix(got.Writer.Tool), got.Fix)
			}
		})
	}

	nobody := workloadSetBy("argocd-controller")
	nobody.ManagedFields = nil
	assert.Equal(t, Leaves, checkerOn(t, interceptor.Funcs{}).Replicas(context.Background(), nobody).Undo,
		"replicas Git doesn't set, as after a scale, aren't undone")
}

// Argo CD owns the replicas it applied even once it's told to ignore them,
// so its Application and argocd-cm say whether it will set them again: it
// leaves them when it ignores them, in the Application or argocd-cm, and
// its syncs respect that. Only the missing step is given as the fix.
func TestChecker_ArgoCD(t *testing.T) {
	tests := []struct {
		name    string
		spec    map[string]any
		config  map[string]string
		want    Undo
		wantFix []string
	}{
		{name: "ignored in the Application, and respected", want: Leaves,
			spec: map[string]any{"syncPolicy": respecting, "ignoreDifferences": ignoring(replicasOfDeployments)}},
		{name: "ignored in the Application by a jq path and a glob", want: Leaves,
			spec: map[string]any{"syncPolicy": respecting, "ignoreDifferences": ignoring(map[string]any{
				"group": "*", "kind": "*", "jqPathExpressions": []any{" .spec.replicas "}})}},
		{name: "ignored in the Application for its whole spec", want: Leaves,
			spec: map[string]any{"syncPolicy": respecting, "ignoreDifferences": ignoring(map[string]any{
				"group": "apps", "kind": "Deployment", "name": "api", "jsonPointers": []any{"/spec"}})}},
		{name: "ignored in argocd-cm, as the guide says", want: Leaves, spec: map[string]any{"syncPolicy": respecting},
			config: map[string]string{
				"resource.customizations.ignoreDifferences.apps_Deployment":  "jsonPointers:\n  - /spec/replicas\n",
				"resource.customizations.ignoreDifferences.apps_StatefulSet": "jsonPointers:\n  - /spec/replicas\n",
			}},
		{name: "ignored in argocd-cm for all kinds", want: Leaves, spec: map[string]any{"syncPolicy": respecting},
			config: map[string]string{"resource.customizations.ignoreDifferences.all": "jsonPointers: [/spec/replicas]"}},
		{name: "ignored in argocd-cm's older block", want: Leaves, spec: map[string]any{"syncPolicy": respecting},
			config: map[string]string{"resource.customizations": "apps/Deployment:\n  ignoreDifferences: |\n" +
				"    jsonPointers:\n    - /spec/replicas\n"}},
		{name: "ignored, but syncs don't respect it", want: Undoes,
			spec: map[string]any{"ignoreDifferences": ignoring(replicasOfDeployments)},
			wantFix: []string{"Add RespectIgnoreDifferences=true to the syncPolicy.syncOptions of Argo CD " +
				"application argocd/shop, so syncs leave the replicas it ignores."}},
		{name: "respected, but nothing is ignored", want: Undoes, spec: map[string]any{"syncPolicy": respecting},
			config: map[string]string{}, wantFix: argoIgnoreFix},
		{name: "another workload's replicas ignored", want: Undoes, wantFix: argoIgnoreFix,
			spec: map[string]any{"syncPolicy": respecting, "ignoreDifferences": ignoring(
				map[string]any{"group": "apps", "kind": "StatefulSet", "jsonPointers": []any{"/spec/replicas"}},
				map[string]any{"group": "apps", "kind": "Deployment", "name": "web", "jsonPointers": []any{"/spec/replicas"}},
				map[string]any{"group": "apps", "kind": "Deployment", "jsonPointers": []any{"/spec/template"}})},
			config: map[string]string{"resource.customizations.ignoreDifferences.apps_StatefulSet": "jsonPointers: [/spec/replicas]"}},
		{name: "neither", want: Undoes, spec: map[string]any{}, config: map[string]string{}, wantFix: Fix(ArgoCD)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []client.Object{application("argocd", tt.spec)}
			if tt.config != nil {
				objs = append(objs, argoConfig(tt.config))
			}

			got := checkerOn(t, interceptor.Funcs{}, objs...).Replicas(context.Background(),
				workloadSetBy("argocd-controller"))

			assert.Equal(t, tt.want, got.Undo, got.Unread)
			assert.Equal(t, "argocd/shop", got.App)
			if tt.want == Undoes {
				assert.Equal(t, tt.wantFix, got.Fix)
				assert.Empty(t, got.Unread)
			}
		})
	}
}

func forbid(match func(client.Object) bool) interceptor.Funcs {
	forbidden := apierrors.NewForbidden(schema.GroupResource{}, "", nil)
	return interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
			opts ...client.GetOption) error {
			if match(obj) {
				return forbidden
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*unstructured.UnstructuredList); ok && match(&unstructured.Unstructured{}) {
				return forbidden
			}
			return c.List(ctx, list, opts...)
		},
	}
}

func isApplication(obj client.Object) bool {
	_, ok := obj.(*unstructured.Unstructured)
	return ok
}

func isConfigMap(obj client.Object) bool {
	_, ok := obj.(*corev1.ConfigMap)
	return ok
}

// What can't be read leaves it unknown, with why, so the warning can say
// "if you haven't already" rather than claim the fix is missing. A missing
// step that can be read is still missing.
func TestChecker_ArgoCDWhenItCantTell(t *testing.T) {
	ignoredNowhere := map[string]any{"syncPolicy": respecting}
	untracked := workloadSetBy("argocd-controller")
	untracked.Annotations = nil
	tests := []struct {
		name       string
		workload   *appsv1.Deployment
		funcs      interceptor.Funcs
		objs       []client.Object
		want       Undo
		wantUnread string
	}{
		{name: "argocd-cm can't be read", funcs: forbid(isConfigMap),
			objs: []client.Object{application("argocd", ignoredNowhere)}, want: MayUndo,
			wantUnread: "your access doesn't allow reading Argo CD's argocd-cm ConfigMap"},
		{name: "argocd-cm can't be read, but syncs don't respect ignores anyway", funcs: forbid(isConfigMap),
			objs: []client.Object{application("argocd", map[string]any{})}, want: Undoes},
		{name: "no argocd-cm where Argo CD would be", want: MayUndo,
			objs:       []client.Object{application("argocd", ignoredNowhere)},
			wantUnread: "no argocd-cm ConfigMap is in argocd"},
		{name: "no argocd-cm beside an Application in another namespace", want: MayUndo,
			workload: func() *appsv1.Deployment {
				d := workloadSetBy("argocd-controller")
				d.Annotations["argocd.argoproj.io/tracking-id"] = "team-a_shop:apps/Deployment:orders/api"
				return d
			}(),
			objs:       []client.Object{application("team-a", ignoredNowhere)},
			wantUnread: "no argocd-cm ConfigMap is in team-a or argocd"},
		{name: "Applications can't be read", funcs: forbid(isApplication), want: MayUndo,
			wantUnread: "your access doesn't allow reading Argo CD's Applications"},
		{name: "no such Application", want: MayUndo, wantUnread: "no Argo CD Application named shop is in this cluster"},
		{name: "not tracked", workload: untracked, want: MayUndo,
			wantUnread: "it has no Argo CD tracking annotation or label naming its Application"},
		{name: "Argo CD in another cluster", want: MayUndo,
			funcs: interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList,
				...client.ListOption) error {
				return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "argoproj.io", Kind: "Application"}}
			}},
			wantUnread: "this cluster has no Argo CD Applications, so Argo CD may run in another"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := tt.workload
			if workload == nil {
				workload = workloadSetBy("argocd-controller")
			}

			got := checkerOn(t, tt.funcs, tt.objs...).Replicas(context.Background(), workload)

			assert.Equal(t, tt.want, got.Undo)
			assert.Equal(t, tt.wantUnread, got.Unread)
			assert.NotEmpty(t, got.Fix)
		})
	}
}

// The Application is found by the name Argo CD tracks the workload by, in
// the namespace that name gives, or wherever it is. Without access to every
// namespace, Argo CD's own is tried.
func TestChecker_FindsTheApplication(t *testing.T) {
	leaving := map[string]any{"syncPolicy": respecting, "ignoreDifferences": ignoring(replicasOfDeployments)}
	byLabel := func(key, value string) *appsv1.Deployment {
		d := workloadSetBy("argocd-controller")
		d.Annotations = nil
		d.Labels = map[string]string{key: value}
		return d
	}
	tests := []struct {
		name     string
		workload *appsv1.Deployment
		app      *unstructured.Unstructured
		funcs    interceptor.Funcs
		wantApp  string
	}{
		{name: "by its tracking annotation", workload: workloadSetBy("argocd-controller"),
			app: application("gitops", leaving), wantApp: "gitops/shop"},
		{name: "in another namespace, named namespace_name", app: application("team-a", leaving),
			workload: byLabel("app.kubernetes.io/instance", "team-a_shop"), wantApp: "team-a/shop"},
		{name: "by Argo CD's own instance label", workload: byLabel("argocd.argoproj.io/instance", "shop"),
			app: application("argocd", leaving), wantApp: "argocd/shop"},
		{name: "without access to every namespace", workload: workloadSetBy("argocd-controller"),
			app: application("argocd", leaving), wantApp: "argocd/shop",
			funcs: interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList,
				...client.ListOption) error {
				return apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "", nil)
			}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkerOn(t, tt.funcs, tt.app).Replicas(context.Background(), tt.workload)

			assert.Equal(t, Leaves, got.Undo, got.Unread)
			assert.Equal(t, tt.wantApp, got.App)
		})
	}
}

// enable --all checks a namespace's workloads in one go, so the
// Applications and argocd-cm are read once between them.
func TestChecker_ReadsArgoCDOnce(t *testing.T) {
	reads := 0
	counting := interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
			opts ...client.GetOption) error {
			reads++
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			reads++
			return c.List(ctx, list, opts...)
		},
	}
	checker := checkerOn(t, counting, application("argocd", map[string]any{"syncPolicy": respecting}),
		argoConfig(map[string]string{"resource.customizations.ignoreDifferences.all": "jsonPointers: [/spec/replicas]"}))

	for range 5 {
		require.Equal(t, Leaves, checker.Replicas(context.Background(), workloadSetBy("argocd-controller")).Undo)
	}

	assert.Equal(t, 2, reads, "one list of Applications and one read of argocd-cm")
}
