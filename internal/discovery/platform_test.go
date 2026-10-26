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

package discovery

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

func idleIn(name, namespace string, labels map[string]string) []runtime.Object {
	objs := deploymentWithRollout(name, namespace, 1, 30*24*time.Hour)
	objs[0].(*appsv1.Deployment).Labels = labels
	return append(objs, makePodMetrics(name, namespace, "1m", "20Mi"))
}

func namespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// The cluster's own infrastructure isn't an app to pause, so it's left out
// of the workloads, the totals and every saving, and named in a note:
// Kubernetes' namespaces, Napwake's, wherever it's installed, and
// well-known infrastructure by its namespace or its labels.
func TestScanCluster_LeavesOutThePlatform(t *testing.T) {
	objs := make([]runtime.Object, 0, 24)
	objs = append(objs, idleIn("coredns", "kube-flannel", nil)...)
	objs = append(objs, idleIn("napwake", "ops", map[string]string{"app.kubernetes.io/name": "napwake"})...)
	objs = append(objs, idleIn("napwake-doorman", "ops", nil)...)
	objs = append(objs, idleIn("webhook", "cert-manager", nil)...)
	objs = append(objs, idleIn("controller", "edge", map[string]string{"helm.sh/chart": "ingress-nginx-4.10.0"})...)
	objs = append(objs, idleIn("checkout", "preview-42", nil)...)
	objs = append(objs, namespace("kube-flannel", nil), namespace("ops", nil), namespace("cert-manager", nil),
		namespace("edge", nil), namespace("preview-42", nil))
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithRuntimeObjects(objs...).Build()

	report, err := NewScanner(c, c).ScanCluster(context.Background(),
		scanOptions("cert-manager", "edge", "kube-flannel", "ops", "preview-42"))

	require.NoError(t, err)
	require.Len(t, report.Workloads, 1)
	assert.Equal(t, "checkout", report.Workloads[0].Name)
	assert.Equal(t, 1, report.Totals.Workloads)
	assert.Equal(t, 1, report.Totals.Idle)
	platform := map[string]string{}
	for _, w := range report.Platform {
		platform[w.Namespace+"/"+w.Name] = w.Platform
	}
	assert.Equal(t, map[string]string{
		"kube-flannel/coredns": "Kubernetes",
		"ops/napwake":          "Napwake",
		"ops/napwake-doorman":  "Napwake",
		"cert-manager/webhook": "cert-manager",
		"edge/controller":      "ingress-nginx",
	}, platform)
	assert.Contains(t, report.Notes, "5 infrastructure workloads are left out of the totals and next steps: "+
		"ops (2), cert-manager (1), edge (1) and 1 more in 1 namespace. Napwake never pauses Kubernetes' own workloads or "+
		"itself; anything else, infrastructure included, pauses only if you opt it in",
		"what the scan leaves out, then, apart, what Napwake would pause")
}

// What the scan leaves out as Kubernetes', which it says Napwake never
// pauses, is exactly what the operator always protects.
func TestNamespacePlatform_KubernetesIsWhatTheOperatorProtects(t *testing.T) {
	for _, ns := range []string{"kube-system", "kube-flannel", "openshift-dns", "openshift-monitoring",
		"cattle-system", "cattle-fleet-system", "gke-managed-system", "gmp-system", "gmp-public",
		"kubeflow", "openshift", "preview-gke-1", "preview-42", "napwake-system", "monitoring"} {
		assert.Equal(t, v1alpha1.Protected(ns, nil, nil), namespacePlatform(ns) == PlatformKubernetes, ns)
	}
}

func TestPlatformNote_One(t *testing.T) {
	assert.Equal(t, "1 infrastructure workload is left out of the totals and next steps: argocd (1). Napwake "+
		"never pauses Kubernetes' own workloads or itself; anything else, infrastructure included, pauses only "+
		"if you opt it in", platformNote([]Workload{{Namespace: "argocd", Name: "argocd-server", Platform: "argocd"}}))
}

// A namespace labelled allow-protected is one Napwake manages, so it's
// scanned like any other, and so is a workload someone already has
// Napwake managing. An app's dependency on Kubernetes isn't listed, as
// Napwake never pauses it, but one on other infrastructure is, as
// Napwake pauses that once it's opted in.
func TestScanCluster_PlatformExceptions(t *testing.T) {
	objs := make([]runtime.Object, 0, 24)
	objs = append(objs, idleIn("coredns", "kube-flannel", nil)...)
	objs = append(objs, idleIn("dns", "kube-system", nil)...)
	objs = append(objs, idleIn("grafana", "monitoring", nil)...)
	objs = append(objs, idleIn("prom", "monitoring", nil)...)
	objs = append(objs, managedFor("grafana", v1alpha1.ManagedWorkloadStatus{}))
	objs[len(objs)-1].(*v1alpha1.ManagedWorkload).Namespace = "monitoring"
	objs = append(objs, depService("monitoring", "prom", false, map[string]string{"app": "prom"}),
		depService("kube-system", "dns", false, map[string]string{"app": "dns"}),
		appWithEnv("api", []corev1.EnvVar{
			{Name: "PROM", Value: "http://prom.monitoring:9090"},
			{Name: "DNS", Value: "dns.kube-system:53"},
		}, nil),
		namespace("kube-flannel", map[string]string{v1alpha1.LabelAllowProtected: v1alpha1.True}),
		namespace("kube-system", nil), namespace("monitoring", nil), namespace(testNamespace, nil))
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithRuntimeObjects(objs...).Build()

	report, err := NewScanner(c, c).ScanCluster(context.Background(),
		scanOptions("kube-flannel", "kube-system", "monitoring", testNamespace))

	require.NoError(t, err)
	got := byName(report)
	assert.Contains(t, got, "coredns", "its namespace allows Napwake in")
	assert.Contains(t, got, "grafana", "Napwake already manages it")
	assert.NotContains(t, got, "prom")
	assert.NotContains(t, got, "dns")
	require.Contains(t, got, "api")
	deps := make([]string, 0, len(got["api"].Dependencies))
	for _, d := range got["api"].Dependencies {
		deps = append(deps, d.Namespace+"/"+d.Name)
	}
	assert.Equal(t, []string{"monitoring/prom"}, deps)
}

// Infrastructure is left out by the scan's guess, not an operator rule, so
// a namespace opted in with the managed label, such as a preview
// environment with its own Vault, is counted in full: the operator pauses
// all of it. Napwake's own components are still left out.
func TestScanCluster_CountsInfrastructureInAnOptedInNamespace(t *testing.T) {
	objs := make([]runtime.Object, 0, 16)
	objs = append(objs, idleIn("vault", "preview-7", map[string]string{"app": "vault"})...)
	objs = append(objs, idleIn("grafana", "monitoring", nil)...)
	objs = append(objs, idleIn("napwake", "preview-7", map[string]string{
		"control-plane": "controller-manager", "app.kubernetes.io/name": "napwake"})...)
	objs = append(objs, idleIn("checkout", "preview-8", nil)...)
	objs = append(objs, idleIn("vault", "preview-8", map[string]string{"app": "vault"})...)
	objs = append(objs,
		namespace("preview-7", map[string]string{v1alpha1.LabelManaged: v1alpha1.True,
			v1alpha1.LabelAllowProtected: v1alpha1.True}),
		namespace("monitoring", map[string]string{v1alpha1.LabelManaged: v1alpha1.True}),
		namespace("preview-8", nil))
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithRuntimeObjects(objs...).Build()

	report, err := NewScanner(c, c).ScanCluster(context.Background(),
		scanOptions("monitoring", "preview-7", "preview-8"))

	require.NoError(t, err)
	got := byName(report)
	assert.Contains(t, got, "grafana", "its namespace is opted in")
	assert.Contains(t, got, "checkout")
	platform := map[string]string{}
	for _, w := range report.Platform {
		platform[w.Namespace+"/"+w.Name] = w.Platform
	}
	assert.Equal(t, map[string]string{
		"preview-7/napwake": PlatformNapwake,
		"preview-8/vault":   "vault",
	}, platform)
	assert.Equal(t, 3, report.Totals.Workloads)
}

// The operator never pauses its own components, so in a namespace labelled
// allow-protected that Napwake shares with apps, its Deployments are still
// Napwake's while the apps beside them are scanned.
func TestScanCluster_NapwakeInAnAllowedNamespace(t *testing.T) {
	objs := make([]runtime.Object, 0, 16)
	objs = append(objs, idleIn("controller-manager", "shared", map[string]string{
		"control-plane": "controller-manager", "app.kubernetes.io/name": "napwake"})...)
	objs = append(objs, idleIn("napwake-doorman", "shared", map[string]string{
		"control-plane": "doorman", "helm.sh/chart": "napwake-0.4.0"})...)
	objs = append(objs, idleIn("checkout", "shared", nil)...)
	objs = append(objs, namespace("shared", map[string]string{v1alpha1.LabelAllowProtected: v1alpha1.True}))
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithRuntimeObjects(objs...).Build()

	report, err := NewScanner(c, c).ScanCluster(context.Background(), scanOptions("shared"))

	require.NoError(t, err)
	assert.Equal(t, []string{"checkout"}, names(report.Workloads))
	assert.Equal(t, 1, report.Totals.Workloads)
	platform := map[string]string{}
	for _, w := range report.Platform {
		platform[w.Name] = w.Platform
	}
	assert.Equal(t, map[string]string{
		"controller-manager": PlatformNapwake,
		"napwake-doorman":    PlatformNapwake,
	}, platform)
}

func names(workloads []Workload) []string {
	out := make([]string, 0, len(workloads))
	for _, w := range workloads {
		out = append(out, w.Name)
	}
	return out
}

func TestChartName(t *testing.T) {
	for label, want := range map[string]string{
		"ingress-nginx-4.10.0":     "ingress-nginx",
		"cert-manager-v1.14.5":     "cert-manager",
		"kube-prometheus-stack-58": "kube-prometheus-stack",
		"my-app":                   "",
		"grafana-":                 "",
		"":                         "",
	} {
		assert.Equal(t, want, chartName(label), label)
	}
}

// The note's counts add up: past the first three namespaces, it says how
// many workloads the rest hold, and in how many namespaces (P52).
func TestPlatformNote_CountsAddUp(t *testing.T) {
	platform := make([]Workload, 0, 19)
	for ns, n := range map[string]int{"argocd": 7, "gmp-system": 3, "keda": 3, "kube-system": 2, "monitoring": 2, "napwake-system": 2} {
		for i := range n {
			platform = append(platform, Workload{Namespace: ns, Name: fmt.Sprint(i)})
		}
	}

	assert.Equal(t, "19 infrastructure workloads are left out of the totals and next steps: argocd (7), "+
		"gmp-system (3), keda (3) and 6 more in 3 namespaces. Napwake never pauses Kubernetes' own workloads or "+
		"itself; anything else, infrastructure included, pauses only if you opt it in", platformNote(platform))
}
