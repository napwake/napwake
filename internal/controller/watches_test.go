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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

// withWorkloadIndexes gives a fake API server the indexes the watches look
// ManagedWorkloads up by, as SetupWithManager gives the cache.
func withWorkloadIndexes(b *fake.ClientBuilder) *fake.ClientBuilder {
	for field, index := range workloadIndexes() {
		b = b.WithIndex(&v1alpha1.ManagedWorkload{}, field, index)
	}
	return b
}

func drainQueue(q workqueue.TypedRateLimitingInterface[reconcile.Request]) []reconcile.Request {
	var got []reconcile.Request
	for q.Len() > 0 {
		req, _ := q.Get()
		q.Done(req)
		got = append(got, req)
	}
	return got
}

// A dependency dropped from dependsOn re-checks the hold its dependent took
// part in, as one still listed does: an update enqueues the related
// workloads from before the change as well as after it.
func TestRelatedHandler_EnqueuesDependenciesBeforeAndAfter(t *testing.T) {
	postgres := depWorkload("default", "postgres", v1alpha1.TargetKindStatefulSet, v1alpha1.PhaseRunning)
	redis := depWorkload("default", "redis", v1alpha1.TargetKindStatefulSet, v1alpha1.PhaseRunning)
	before := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, postgresRef(),
		v1alpha1.DependencyRef{Kind: v1alpha1.TargetKindStatefulSet, Name: "redis"})
	after := before.DeepCopy()
	after.Spec.DependsOn = []v1alpha1.DependencyRef{postgresRef()}
	r := depReconciler(t, &stubPauser{}, after, postgres, redis)
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer q.ShutDown()

	r.relatedHandler().Update(context.Background(), event.UpdateEvent{ObjectOld: before, ObjectNew: after}, q)

	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "postgres"}},
		{NamespacedName: types.NamespacedName{Namespace: "default", Name: "redis"}},
	}, drainQueue(q), "redis, no longer depended on, re-checks too")
}

// A ManagedWorkload's own status writes, such as cost and activity
// flushes, every few minutes for every workload, concern no other workload,
// so they don't make every related one reconcile.
func TestRelationsChanged(t *testing.T) {
	base := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, postgresRef())
	tests := []struct {
		name   string
		change func(w *v1alpha1.ManagedWorkload)
		passes bool
	}{
		{name: "cost and activity", change: func(w *v1alpha1.ManagedWorkload) {
			w.Status.Cost = &v1alpha1.CostStatus{CostThisMonth: "$1.00"}
			w.Status.Activity.LastActivitySource = v1alpha1.ActivitySourceCPU
		}},
		{name: "phase", passes: true, change: func(w *v1alpha1.ManagedWorkload) { w.Status.Phase = v1alpha1.PhaseIdle }},
		{name: "spec", passes: true, change: func(w *v1alpha1.ManagedWorkload) { w.Generation++ }},
		{name: "learned dependencies", passes: true, change: func(w *v1alpha1.ManagedWorkload) {
			w.Status.LearnedDependencies = &v1alpha1.LearnedDependencies{From: "abc"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := base.DeepCopy()
			tt.change(changed)
			assert.Equal(t, tt.passes, relationsChanged().Update(event.UpdateEvent{ObjectOld: base, ObjectNew: changed}))
		})
	}
}

// A Deployment or StatefulSet no ManagedWorkload manages or depends on,
// whose status churns through every rollout, enqueues nothing.
func TestFindWorkloadsForTarget_UnrelatedTargetEnqueuesNothing(t *testing.T) {
	api := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, postgresRef())
	r := depReconciler(t, &stubPauser{}, api)

	assert.Empty(t, r.findWorkloadsForTarget(context.Background(), targetDeployment("unrelated", "default")))
	require.Len(t, r.findWorkloadsForTarget(context.Background(), targetDeployment("api", "default")), 1)
}
