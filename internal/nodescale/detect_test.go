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

package nodescale

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var nodePoolKind = schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodePool"}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	return s
}

// fakeCluster is a cluster with objs, and Karpenter's NodePool CRD when
// karpenter is set.
func fakeCluster(t *testing.T, karpenter bool, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	s := testScheme(t)
	mapper := meta.NewDefaultRESTMapper(nil)
	for gvk := range s.AllKnownTypes() {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Node"), meta.RESTScopeRoot)
	if karpenter {
		mapper.Add(nodePoolKind, meta.RESTScopeRoot)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithRESTMapper(mapper).WithObjects(objs...).
		WithInterceptorFuncs(funcs).Build()
	if karpenter {
		return c
	}
	// The fake client lists any kind it's asked for; the API server
	// doesn't know one whose CRD isn't installed.
	return interceptor.NewClient(c, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch,
		list client.ObjectList, opts ...client.ListOption) error {
		if list.GetObjectKind().GroupVersionKind().Group == nodePoolKind.Group {
			return &meta.NoKindMatchError{GroupKind: nodePoolKind.GroupKind()}
		}
		return c.List(ctx, list, opts...)
	}})
}

func node(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func nodePool(name, policy, after string) *unstructured.Unstructured {
	p := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"disruption": map[string]any{}}}}
	p.SetGroupVersionKind(nodePoolKind)
	p.SetName(name)
	if policy != "" {
		_ = unstructured.SetNestedField(p.Object, policy, "spec", "disruption", "consolidationPolicy")
	}
	if after != "" {
		_ = unstructured.SetNestedField(p.Object, after, "spec", "disruption", "consolidateAfter")
	}
	return p
}

func statusMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cluster-autoscaler-status", Namespace: "kube-system"}}
}

func deployment(namespace, name string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
}

// forbid refuses every read of the kinds named, as RBAC would.
func forbid(kinds ...string) interceptor.Funcs {
	s := clientgoscheme.Scheme
	denied := func(obj runtime.Object) error {
		kind := obj.GetObjectKind().GroupVersionKind().Kind
		if kind == "" {
			gvk, err := apiutil.GVKForObject(obj, s)
			if err != nil {
				return err
			}
			kind = gvk.Kind
		}
		if slices.Contains(kinds, kind) {
			return apierrors.NewForbidden(schema.GroupResource{Resource: kind}, "", errors.New("denied"))
		}
		return nil
	}
	return interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
			opts ...client.GetOption) error {
			if err := denied(obj); err != nil {
				return err
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := denied(list); err != nil {
				return err
			}
			return c.List(ctx, list, opts...)
		},
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name      string
		karpenter bool
		funcs     interceptor.Funcs
		objs      []client.Object
		want      []Scaler
		known     bool
		unread    []string
	}{
		{
			name: "nothing found reads as none",
			objs: []client.Object{node("a", nil)}, known: true,
		},
		{
			name: "Karpenter from its nodes", karpenter: true, known: true,
			objs: []client.Object{node("a", map[string]string{"karpenter.sh/nodepool": "default"}),
				nodePool("default", "", "30s")},
			want: []Scaler{{Kind: Karpenter, Evidence: "1 node labelled karpenter.sh/nodepool"}},
		},
		{
			name: "Karpenter from its CRD before it has nodes", karpenter: true, known: true,
			objs: []client.Object{node("a", nil)},
			want: []Scaler{{Kind: Karpenter, Evidence: "the nodepools.karpenter.sh CRD"}},
		},
		{
			name: "EKS Auto Mode, whose NodePools are Karpenter's", karpenter: true, known: true,
			objs: []client.Object{
				node("a", map[string]string{"eks.amazonaws.com/compute-type": "auto", "karpenter.sh/nodepool": "general-purpose"}),
				node("b", map[string]string{"eks.amazonaws.com/compute-type": "auto", "karpenter.sh/nodepool": "system"}),
			},
			want: []Scaler{{Kind: EKSAutoMode, Evidence: "2 nodes labelled eks.amazonaws.com/compute-type=auto"}},
		},
		{
			name: "Cluster Autoscaler on AKS from its status ConfigMap", known: true,
			objs: []client.Object{node("a", map[string]string{"kubernetes.azure.com/agentpool": "nodepool1"}), statusMap()},
			want: []Scaler{{Kind: ClusterAutoscaler, Provider: "AKS",
				Evidence: "the cluster-autoscaler-status ConfigMap in kube-system"}},
		},
		{
			name: "GKE Autopilot from its node names", known: true,
			objs: []client.Object{node("gk3-dev-pool-2-abc", map[string]string{"cloud.google.com/gke-nodepool": "pool-2"}),
				statusMap()},
			want: []Scaler{{Kind: GKEAutopilot, Provider: "GKE",
				Evidence: "the cluster-autoscaler-status ConfigMap, and nodes named gk3-*"}},
		},
		{
			name: "GKE Standard is Cluster Autoscaler", known: true,
			objs: []client.Object{node("gke-dev-default-pool-abc", map[string]string{"cloud.google.com/gke-nodepool": "default-pool"}),
				statusMap()},
			want: []Scaler{{Kind: ClusterAutoscaler, Provider: "GKE",
				Evidence: "the cluster-autoscaler-status ConfigMap in kube-system"}},
		},
		{
			name: "a Deployment named for Cluster Autoscaler is a hint", known: true,
			objs: []client.Object{node("a", map[string]string{"eks.amazonaws.com/nodegroup": "ng"}),
				deployment("kube-system", "ca-aws-cluster-autoscaler"), deployment("shop", "cluster-autoscaler")},
			want: []Scaler{{Kind: ClusterAutoscaler, Provider: "EKS", Hint: true,
				Evidence: "a Deployment named ca-aws-cluster-autoscaler in kube-system, which has written no status"}},
		},
		{
			name: "nothing readable is unknown, never none", karpenter: true,
			funcs: forbid("NodePoolList", "NodeList", "ConfigMap", "DeploymentList"),
			unread: []string{"Karpenter NodePools", "nodes",
				"the cluster-autoscaler-status ConfigMap in kube-system"},
		},
		{
			name:  "an autoscaler found is known though some reads were refused",
			funcs: forbid("NodeList"), objs: []client.Object{statusMap()}, known: true,
			want:   []Scaler{{Kind: ClusterAutoscaler, Evidence: "the cluster-autoscaler-status ConfigMap in kube-system"}},
			unread: []string{"nodes"},
		},
		{
			name: "only the Deployment fallback refused is still unknown", funcs: forbid("DeploymentList"),
			unread: []string{"Deployments in kube-system"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Detect(context.Background(), fakeCluster(t, tt.karpenter, tt.funcs, tt.objs...))

			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Scalers)
			assert.Equal(t, tt.known, got.Known())
			assert.Equal(t, tt.unread, got.Unread)
		})
	}
}

func TestDetect_ReadsNodePoolConsolidation(t *testing.T) {
	c := fakeCluster(t, true, interceptor.Funcs{},
		nodePool("default", "", ""), nodePool("batch", "WhenEmpty", "Never"), nodePool("web", "Balanced", "5m"))

	got, err := Detect(context.Background(), c)

	require.NoError(t, err)
	assert.Equal(t, []NodePool{
		{Name: "batch", ConsolidationPolicy: ConsolidateWhenEmpty, ConsolidateAfter: ConsolidateAfterNever},
		{Name: "default", ConsolidationPolicy: ConsolidateWhenEmptyOrUnderutilized},
		{Name: "web", ConsolidationPolicy: ConsolidateBalanced, ConsolidateAfter: "5m"},
	}, got.NodePools)
}

func TestDetect_FailsOnOtherErrors(t *testing.T) {
	broken := interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList,
		...client.ListOption) error {
		return errors.New("connection refused")
	}}

	_, err := Detect(context.Background(), fakeCluster(t, false, broken))

	assert.ErrorContains(t, err, "connection refused")
}
