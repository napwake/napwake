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

package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/napwake/napwake/internal/gitops"
)

// binaryAssets is the first envtest install under bin/k8s, or "" to leave
// it to KUBEBUILDER_ASSETS.
func binaryAssets() string {
	entries, err := os.ReadDir(filepath.Join("..", "..", "bin", "k8s"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join("..", "..", "bin", "k8s", e.Name())
		}
	}
	return ""
}

// The unit tests build managed fields by hand; this pins what the API
// server really records when replicas are set through the scale
// subresource, as Napwake's pause, kubectl scale, an HPA and KEDA do.
func TestReplicasWriter_APIServer(t *testing.T) {
	env := &envtest.Environment{BinaryAssetsDirectory: binaryAssets()}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Stop()) })
	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	labels := map[string]string{"app": "api"}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](2),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "api:v1"}}}},
		},
	}
	require.NoError(t, c.Create(ctx, deploy, client.FieldOwner("kluctl")))

	writer, ok := gitops.ReplicasWriter(deploy.GetManagedFields())
	require.True(t, ok)
	assert.Equal(t, gitops.Kluctl, writer.Tool, "a write of the workload itself records its writer")

	scale := &autoscalingv1.Scale{}
	require.NoError(t, c.SubResource("scale").Get(ctx, deploy, scale))
	scale.Spec.Replicas = 0
	require.NoError(t, c.SubResource("scale").Update(ctx, deploy, client.WithSubResourceBody(scale),
		client.FieldOwner("napwake")))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(deploy), deploy))

	writer, ok = gitops.ReplicasWriter(deploy.GetManagedFields())
	assert.False(t, ok, "a scale takes replicas from their owner, and records no new one: %+v", writer)
}

// The fake client can't tell an Application kind the API server doesn't
// serve from one it does, so this checks both against a real one: without
// Argo CD's CRD, as where Argo CD runs in another cluster, the check can't
// tell; with it, it reads the Application and argocd-cm as Argo CD does.
func TestChecker_APIServer(t *testing.T) {
	env := &envtest.Environment{BinaryAssetsDirectory: binaryAssets()}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Stop()) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	require.NoError(t, err)

	labels := map[string]string{"app": "api"}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default",
			Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "shop:apps/Deployment:default/api"}},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](2),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "api:v1"}}}},
		},
	}
	require.NoError(t, c.Create(ctx, deploy, client.FieldOwner("argocd-controller")))

	got := gitops.NewChecker(c).Replicas(ctx, deploy)
	assert.Equal(t, gitops.MayUndo, got.Undo)
	assert.Equal(t, "this cluster has no Argo CD Applications, so Argo CD may run in another", got.Unread)

	_, err = envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{Paths: []string{filepath.Join("testdata",
		"argocd-application-crd.yaml")}})
	require.NoError(t, err)
	c, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	require.NoError(t, err)
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "argocd"}}))
	app := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"syncPolicy": map[string]any{"syncOptions": []any{"RespectIgnoreDifferences=true"}}}}}
	app.SetAPIVersion("argoproj.io/v1alpha1")
	app.SetKind("Application")
	app.SetNamespace("argocd")
	app.SetName("shop")
	require.NoError(t, c.Create(ctx, app))
	require.NoError(t, c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "argocd-cm", Namespace: "argocd"},
		Data: map[string]string{"resource.customizations.ignoreDifferences.apps_Deployment": "jsonPointers:\n- /spec/replicas\n"}}))

	got = gitops.NewChecker(c).Replicas(ctx, deploy)
	assert.Equal(t, gitops.Leaves, got.Undo, got.Unread)
	assert.Equal(t, "argocd/shop", got.App)
}
