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

package nodescale

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// What the operator publishes says whether anything scales nodes down, and
// never claims "none" when it couldn't look.
func TestReporter_PublishesWhatScalesTheNodes(t *testing.T) {
	tests := []struct {
		name    string
		cluster func(t *testing.T) client.Reader
		want    []string
	}{
		{name: "Karpenter", want: []string{"Karpenter"}, cluster: func(t *testing.T) client.Reader {
			return fakeCluster(t, true, interceptor.Funcs{},
				node("a", map[string]string{labelKarpenterNodePool: "default"}),
				nodePool("default", ConsolidateWhenEmptyOrUnderutilized, "30s"))
		}},
		{name: "nothing found", want: []string{"none"}, cluster: func(t *testing.T) client.Reader {
			return fakeCluster(t, false, interceptor.Funcs{}, node("a", nil))
		}},
		{name: "not allowed to look", want: []string{"unknown"}, cluster: func(t *testing.T) client.Reader {
			return fakeCluster(t, false, forbid("ConfigMap", "DeploymentList", "PartialObjectMetadataList"), node("a", nil))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			r := NewReporter(tt.cluster(t), func(kinds []string) { got = kinds })

			last := r.report(context.Background(), "")

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.want[0], last, "remembered, so an unchanged result isn't logged again")
		})
	}
}

func TestKinds_ListsEachKindOnce(t *testing.T) {
	a := Autoscaler{Scalers: []Scaler{{Kind: ClusterAutoscaler}, {Kind: Karpenter}, {Kind: ClusterAutoscaler}}}
	assert.Equal(t, []string{"Cluster Autoscaler", "Karpenter"}, Kinds(a))
}
