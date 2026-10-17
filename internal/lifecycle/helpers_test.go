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

package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/autoscaler"
)

var scaledObject = schema.GroupVersionKind{Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledObject"}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, autoscalingv2.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// newClient is a fake API server holding objs, which serves KEDA's
// ScaledObjects and the scale subresource of Deployments and StatefulSets.
func newClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	mapper := meta.NewDefaultRESTMapper(nil)
	for _, gvk := range []schema.GroupVersionKind{appsv1.SchemeGroupVersion.WithKind("Deployment"),
		appsv1.SchemeGroupVersion.WithKind("StatefulSet"),
		autoscalingv2.SchemeGroupVersion.WithKind("HorizontalPodAutoscaler"), scaledObject,
		v1alpha1.GroupVersion.WithKind("ManagedWorkload")} {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithRESTMapper(mapper).WithObjects(objs...).Build()
}

func newTestPauser(c client.Client) *Pauser {
	return &Pauser{
		client:      c,
		scaler:      &subResourceScaler{client: c},
		autoscalers: autoscaler.NewFinder(c),
		clock:       func() metav1.Time { return metav1.NewTime(time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)) },
	}
}

// deployment is default/api at replicas, ready of them Ready, by a status
// its controller wrote for its current spec.
func deployment(replicas, ready int32) *appsv1.Deployment {
	labels := map[string]string{"app": "api"}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", Generation: 1},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}},
			},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: ready, ReadyReplicas: ready},
	}
}

// statefulSet is default/api, as deployment is.
func statefulSet(replicas, ready int32) *appsv1.StatefulSet {
	labels := map[string]string{"app": "api"}
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", Generation: 1},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}},
			},
		},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, Replicas: ready, ReadyReplicas: ready},
	}
}

func apiWorkload() *v1alpha1.ManagedWorkload {
	return &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec:       v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"}},
	}
}

func pausedWorkload(previous int32) *v1alpha1.ManagedWorkload {
	w := apiWorkload()
	w.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: previous}
	return w
}

var apiKey = types.NamespacedName{Namespace: "default", Name: "api"}

// replicas is what default/api asks for.
func replicas(t *testing.T, c client.Client) int32 {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), apiKey, &d))
	return *d.Spec.Replicas
}

// settle stands in for the Deployment controller: ready of the replicas
// default/api asks for are Ready, by a status written for its current spec.
func settle(t *testing.T, c client.Client, ready int32) {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), apiKey, &d))
	d.Status.ObservedGeneration = d.Generation
	d.Status.Replicas, d.Status.ReadyReplicas = *d.Spec.Replicas, ready
	require.NoError(t, c.Status().Update(context.Background(), &d))
}

func scaledObjectFor(minReplicas int64) *unstructured.Unstructured {
	so := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"scaleTargetRef": map[string]any{"name": "api"}, "minReplicaCount": minReplicas}}}
	so.SetGroupVersionKind(scaledObject)
	so.SetNamespace("default")
	so.SetName("api-scaler")
	return so
}

func pausedAnnotation(t *testing.T, c client.Client) (string, bool) {
	t.Helper()
	so := &unstructured.Unstructured{}
	so.SetGroupVersionKind(scaledObject)
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "api-scaler"}, so))
	v, ok := so.GetAnnotations()[autoscaler.PausedReplicasAnnotation]
	return v, ok
}
