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

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// gkeAutoNEG is what GKE writes on every Service of a VPC-native cluster.
const gkeAutoNEG = `{"ingress":true}`

func annotatedService(annotations map[string]string) corev1.Service {
	return corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", Annotations: annotations}}
}

// ingressTo is an Ingress with one rule sending / to service.
func ingressTo(service string, annotations map[string]string, className *string) networkingv1.Ingress {
	return networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default", Annotations: annotations},
		Spec: networkingv1.IngressSpec{
			IngressClassName: className,
			Rules: []networkingv1.IngressRule{{IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
					Path: "/", PathType: ptr.To(networkingv1.PathTypePrefix),
					Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
						Name: service, Port: networkingv1.ServiceBackendPort{Number: 80},
					}},
				}}},
			}}},
		},
	}
}

func gceIngressTo(service string) networkingv1.Ingress {
	return ingressTo(service, map[string]string{annotationIngressClass: "gce"}, nil)
}

func TestUsesGKENEG(t *testing.T) {
	defaultBackend := networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default",
			Annotations: map[string]string{annotationIngressClass: "gce"}},
		Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{
			Service: &networkingv1.IngressServiceBackend{Name: "api", Port: networkingv1.ServiceBackendPort{Number: 80}},
		}},
	}
	noRules := networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "example.com"}}}}

	tests := []struct {
		name        string
		annotations map[string]string
		ingresses   []networkingv1.Ingress
		want        bool
	}{
		{name: "no NEG annotation", want: false},
		{
			name:        "GKE's own annotation, with no Ingress using the Service",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG},
			want:        false,
		},
		{
			name:        "GKE's own annotation, with a GKE Ingress using another Service",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG},
			ingresses:   []networkingv1.Ingress{gceIngressTo("web")},
			want:        false,
		},
		{
			name:        "the NEG controller keeps NEGs for it",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG, annotationGKENEGStatus: `{"network_endpoint_groups":{"80":"k8s1-abc-default-api-80-1"},"zones":["europe-west1-b"]}`},
			want:        true,
		},
		{
			name:        "NEGs reported without the annotation that asked for them",
			annotations: map[string]string{annotationGKENEGStatus: `{"network_endpoint_groups":{"80":"k8s1-abc-default-api-80-1"}}`},
			want:        true,
		},
		{
			name:        "only a passthrough load balancer's NEG of nodes",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG, annotationGKENEGStatus: `{"network_endpoint_groups":{"0":"k8s2-abc-default-api-1"},"zones":["europe-west1-b"]}`},
			want:        false,
		},
		{
			name:        "a NEG status that can't be read",
			annotations: map[string]string{annotationGKENEGStatus: `negs`},
			want:        true,
		},
		{
			name:        "standalone NEGs",
			annotations: map[string]string{annotationGKENEG: `{"exposed_ports":{"80":{}}}`},
			want:        true,
		},
		{
			name:        "a NEG annotation that can't be read",
			annotations: map[string]string{annotationGKENEG: `ingress`},
			want:        true,
		},
		{
			name:        "a gce Ingress uses it",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG},
			ingresses:   []networkingv1.Ingress{gceIngressTo("api")},
			want:        true,
		},
		{
			name:        "a gce Ingress's default backend",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG},
			ingresses:   []networkingv1.Ingress{defaultBackend},
			want:        true,
		},
		{
			name:        "a gce-internal Ingress uses it",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG},
			ingresses:   []networkingv1.Ingress{ingressTo("api", map[string]string{annotationIngressClass: "gce-internal"}, nil)},
			want:        true,
		},
		{
			name:        "an Ingress with no class, which GKE serves",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG},
			ingresses:   []networkingv1.Ingress{ingressTo("api", nil, nil)},
			want:        true,
		},
		{
			name:        "an nginx Ingress, by ingressClassName",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG},
			ingresses:   []networkingv1.Ingress{ingressTo("api", nil, ptr.To("nginx"))},
			want:        false,
		},
		{
			name:        "an nginx Ingress, by annotation",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG},
			ingresses:   []networkingv1.Ingress{ingressTo("api", map[string]string{annotationIngressClass: "nginx"}, nil)},
			want:        false,
		},
		{
			name:        "ingressClassName gce, which GKE doesn't read",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG},
			ingresses:   []networkingv1.Ingress{ingressTo("api", nil, ptr.To("gce"))},
			want:        false,
		},
		{
			name:        "a GKE Ingress, with NEGs turned off for the Service",
			annotations: map[string]string{annotationGKENEG: `{"ingress":false}`},
			ingresses:   []networkingv1.Ingress{gceIngressTo("api")},
			want:        false,
		},
		{
			name:      "a GKE Ingress, without the NEG annotation, so through instance groups",
			ingresses: []networkingv1.Ingress{gceIngressTo("api")},
			want:      false,
		},
		{
			name:        "an Ingress rule without HTTP paths",
			annotations: map[string]string{annotationGKENEG: gkeAutoNEG},
			ingresses:   []networkingv1.Ingress{noRules},
			want:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, usesGKENEG(annotatedService(tt.annotations), tt.ingresses))
		})
	}
}
