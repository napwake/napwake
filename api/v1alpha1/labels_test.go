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

package v1alpha1

import "testing"

func TestProtected(t *testing.T) {
	patterns := []string{"prod-*", "production"}
	tests := []struct {
		name      string
		namespace string
		labels    map[string]string
		want      bool
	}{
		{name: "labelled", namespace: "payments", labels: map[string]string{LabelProtected: True}, want: true},
		{name: "a pattern", namespace: "prod-eu", want: true},
		{name: "an exact name", namespace: "production", want: true},
		{name: "allowed anyway", namespace: "prod-eu", labels: map[string]string{LabelAllowProtected: True}},
		{name: "allowed though labelled", namespace: "payments",
			labels: map[string]string{LabelProtected: True, LabelAllowProtected: True}},
		{name: "a label value other than true", namespace: "payments", labels: map[string]string{LabelProtected: "yes"}},
		{name: "kube-system", namespace: "kube-system", want: true},
		{name: "another system namespace", namespace: "kube-node-lease", want: true},
		{name: "a system namespace allowed anyway", namespace: "kube-system",
			labels: map[string]string{LabelAllowProtected: True}},
		{name: "OpenShift's", namespace: "openshift-dns", want: true},
		{name: "Rancher's", namespace: "cattle-system", want: true},
		{name: "GKE's", namespace: "gke-managed-system", want: true},
		{name: "Google Cloud Managed Service for Prometheus's", namespace: "gmp-system", want: true},
		{name: "a platform's allowed anyway", namespace: "openshift-dns",
			labels: map[string]string{LabelAllowProtected: True}},
		{name: "a name only like a system namespace", namespace: "kubeflow"},
		{name: "a platform's name alone", namespace: "openshift"},
		{name: "a platform's name inside another", namespace: "preview-gke-1"},
		{name: "neither", namespace: "preview-42"},
		{name: "a pattern only at the start", namespace: "preview-prod-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Protected(tt.namespace, tt.labels, patterns); got != tt.want {
				t.Errorf("Protected(%q, %v) = %t, want %t", tt.namespace, tt.labels, got, tt.want)
			}
		})
	}
}
