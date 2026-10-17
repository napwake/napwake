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

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/autoscaler"
)

func targetDeploymentOf(t *testing.T, r *Reconciler) *appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "api"}, &d))
	return &d
}

// copyPauseRecord puts a copy of a pause record on default/api, as a pause
// leaves one.
func copyPauseRecord(t *testing.T, r *Reconciler, record string) {
	t.Helper()
	d := targetDeploymentOf(t, r)
	d.Annotations = map[string]string{v1alpha1.AnnotationPausedFrom: record}
	require.NoError(t, r.Update(context.Background(), d))
}

// A paused workload whose ManagedWorkload loses its status, as to a backup
// restored without it, or the ManagedWorkload being created again, is
// taken up from the copy its target carries: Paused again, then woken to
// the replicas it had, with KEDA given back its own paused-replicas value.
// Without it the target would be taken for one scaled to zero outside
// Hybernate and left off for good, held there by KEDA.
func TestPause_StatusLostWhilePausedIsTakenUpFromTheTarget(t *testing.T) {
	so := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"scaleTargetRef": map[string]any{"name": "api"}, "minReplicaCount": int64(1)}}}
	so.SetGroupVersionKind(kedaScaledObject)
	so.SetNamespace("default")
	so.SetName("api-scaler")
	so.SetAnnotations(map[string]string{autoscaler.PausedReplicasAnnotation: "5"})
	workload := withPauseRequested(lifecycleWorkload("api", v1alpha1.PhaseRunning))
	r := lifecycleReconciler(t, workload, 3, interceptor.Funcs{}, so)
	reconcileUntilSettled(t, r)
	require.Equal(t, v1alpha1.PhasePaused, getWorkload(t, r, "api").Status.Phase)
	require.Equal(t, int32(0), targetReplicas(t, r))

	lost := getWorkload(t, r, "api")
	lost.Status = v1alpha1.ManagedWorkloadStatus{}
	require.NoError(t, r.Status().Update(context.Background(), lost))
	reconcileUntilSettled(t, r)

	recovered := getWorkload(t, r, "api")
	require.Equal(t, v1alpha1.PhasePaused, recovered.Status.Phase, "not left off as scaled to zero outside Hybernate")
	require.NotNil(t, recovered.Status.Pause)
	assert.Equal(t, int32(3), recovered.Status.Pause.PreviousReplicas)
	assert.Contains(t, drainEvents(t, r), ReasonPauseRecovered)

	require.NoError(t, r.Update(context.Background(), wokenByActivity(recovered)))
	reconcileUntilSettled(t, r)

	assert.Equal(t, v1alpha1.PhaseRunning, getWorkload(t, r, "api").Status.Phase)
	assert.Equal(t, int32(3), targetReplicas(t, r))
	v, _ := pausedScaledObject(t, r)
	assert.Equal(t, "5", v, "KEDA is given back its own value, not left holding the target at zero")
	assert.NotContains(t, targetDeploymentOf(t, r).Annotations, v1alpha1.AnnotationPausedFrom)
}

// The copy is only taken up while the target is at zero: one scaled up
// since is released, and a copy that can't be read is reported, not acted
// on.
func TestPause_RecordCopyOnARunningTarget(t *testing.T) {
	tests := []struct {
		name   string
		record string
		event  string
	}{
		{name: "scaled up since", record: `{"replicas":3}`},
		{name: "unreadable", record: "3", event: ReasonPauseRecordLost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := lifecycleReconciler(t, lifecycleWorkload("api", v1alpha1.PhaseRunning), 2, interceptor.Funcs{})
			copyPauseRecord(t, r, tt.record)

			_, err := r.Reconcile(context.Background(), reconcileFor("api"))
			require.NoError(t, err)

			got := getWorkload(t, r, "api")
			assert.Equal(t, v1alpha1.PhaseRunning, got.Status.Phase)
			assert.Nil(t, got.Status.Pause)
			assert.Equal(t, int32(2), targetReplicas(t, r))
			if tt.event != "" {
				assert.Contains(t, drainEvents(t, r), tt.event)
				return
			}
			assert.NotContains(t, targetDeploymentOf(t, r).Annotations, v1alpha1.AnnotationPausedFrom)
		})
	}
}
