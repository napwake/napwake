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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

// A duration the API server stores but metav1.Duration can't decode breaks
// every typed read of ManagedWorkloads, so the operator's and the doorman's
// caches never sync. The CRD must accept exactly what Go parses.
func TestCRD_DurationsAreThoseGoCanDecode(t *testing.T) {
	cfg := startEnvtest(t)
	c, err := client.New(cfg, client.Options{Scheme: testScheme(t)})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev"}}))

	fields := map[string][]string{"idleAfter": {"idlePolicy", "idleAfter"}, "maxWait": {"wake", "maxWait"}}
	tests := []struct {
		value string
		valid bool
	}{
		{value: "90m", valid: true},
		{value: "1h30m", valid: true},
		{value: "45s", valid: true},
		{value: "1.5m", valid: true},
		{value: "1d"},
		{value: "2w"},
		{value: "1 h"},
		{value: "1h "},
		{value: "0s"},
		{value: "-5m"},
		{value: "soon"},
	}
	n := 0
	for field, path := range fields {
		for _, tt := range tests {
			t.Run(field+"="+tt.value, func(t *testing.T) {
				n++
				mw := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "hybernate.io/v1alpha1", "kind": "ManagedWorkload",
					"metadata": map[string]any{"name": fmt.Sprintf("w%d", n), "namespace": "dev"},
					"spec": map[string]any{
						"target":     map[string]any{"kind": "Deployment", "name": "api"},
						"prediction": map[string]any{"confidence": int64(75)},
					},
				}}
				require.NoError(t, unstructured.SetNestedField(mw.Object, tt.value, append([]string{"spec"}, path...)...))

				err := c.Create(ctx, mw)
				if tt.valid {
					assert.NoError(t, err)
				} else {
					assert.True(t, apierrors.IsInvalid(err), "want the value rejected, got %v", err)
				}
			})
		}
	}

	var list v1alpha1.ManagedWorkloadList
	assert.NoError(t, c.List(ctx, &list, client.InNamespace("dev")), "every stored ManagedWorkload decodes")
}
