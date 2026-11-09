//go:build sizing

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

package sizing_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/napwake/napwake/internal/controller"
)

// What the operator's cache holds of each Deployment, its pod template cut
// down by controller.TrimPodTemplate, as the chart's values.yaml says.
const deploymentBytes int64 = 9600

// The operator caches every Deployment it watches, managed or not, and the
// pod template is most of each, so its cache cuts the template down to
// what the operator reads. Measured with the Deployment the operator's
// binary is measured with, a chart's with a service mesh, as it was cached
// before, with only its managed fields dropped, and as it is now. A
// StatefulSet's template is cut the same way.
func TestOperatorDeploymentCache(t *testing.T) {
	strip := cache.TransformStripManagedFields()
	trim := func(in any) (any, error) {
		out, err := strip(in)
		if d, ok := out.(*appsv1.Deployment); ok {
			controller.TrimPodTemplate(&d.Spec.Template)
		}
		return out, err
	}
	counts := []int{1_000, 10_000}
	measured := map[string]map[int]result{}
	for _, c := range []struct {
		name      string
		transform func(any) (any, error)
	}{{"whole", strip}, {"trimmed", trim}} {
		t.Run(c.name, func(t *testing.T) {
			api := startAPIServer(t)
			measured[c.name] = api.measure(t, kind{
				name: "deployments", obj: &appsv1.Deployment{}, list: &appsv1.DeploymentList{},
				cache: cache.ByObject{Transform: c.transform}, counts: counts,
				create: func(ctx context.Context, cl client.Client, d int) error {
					return cl.Create(ctx, deployment(d, 1), writer)
				},
				cached: func(n int) int { return n },
				trimmed: func(t *testing.T, list client.ObjectList) {
					d := list.(*appsv1.DeploymentList).Items[0]
					assert.Empty(t, d.ManagedFields, "a cached Deployment keeps no managed fields")
					if c.name == "trimmed" {
						assert.Empty(t, d.Spec.Template.Spec.Containers[0].Image, "nor the images its template runs")
					}
				},
			})
		})
	}
	for _, n := range counts {
		t.Logf("the operator, %d Deployments: %s a Deployment whole, %s trimmed", n, measured["whole"][n],
			measured["trimmed"][n])
	}
	heldTo(t, measured["trimmed"][10_000].median, deploymentBytes, fmt.Sprintf("the operator's bytes a Deployment, at %d",
		10_000))
}
