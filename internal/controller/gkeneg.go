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
	"encoding/json"
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/okedeji/hybernate/internal/doorman"
)

const (
	// annotationGKENEG asks GKE's NEG controller for network endpoint
	// groups of a Service's pods. GKE sets it to {"ingress":true} on every
	// Service of a VPC-native cluster, which asks for NEGs only once a GKE
	// Ingress uses the Service.
	annotationGKENEG = "cloud.google.com/neg"

	annotationGKENEGStatus = doorman.AnnotationGKENEGStatus

	annotationIngressClass = "kubernetes.io/ingress.class"
)

// gkeIngressClasses are the kubernetes.io/ingress.class values GKE's
// ingress controllers serve, and whose backends the NEG controller puts in
// NEGs.
var gkeIngressClasses = map[string]bool{
	"gce":                   true,
	"gce-internal":          true,
	"gce-regional-external": true,
	"gce-multi-cluster":     true,
}

// usesGKENEG reports whether GKE's NEG controller syncs a Service's
// endpoints into NEGs of pods, or is about to. Such a load balancer sends
// traffic straight to the pods the endpoints name, and the NEG controller
// checks each endpoint's pod against the Service in the pod's own
// namespace, which for the doorman is the wrong one: it can fail the
// Service's whole sync on one endpoint it rejects, freezing the load
// balancer until the workload wakes.
//
// A Service counts once the NEG controller lists NEGs of pods for it,
// asks for standalone NEGs with exposed_ports, as GKE Gateway does, or is a
// backend of an Ingress GKE serves while it asks for NEGs for Ingress. The
// {"ingress":true} GKE puts on every Service doesn't count by itself: no
// NEG exists until such an Ingress uses the Service. Annotations that can't
// be parsed count, to stay safe. ingresses are those in the Service's
// namespace.
func usesGKENEG(svc corev1.Service, ingresses []networkingv1.Ingress) bool {
	if doorman.InPodNEGs(svc) {
		return true
	}
	value, ok := svc.Annotations[annotationGKENEG]
	if !ok {
		return false
	}
	var neg struct {
		Ingress      bool                       `json:"ingress"`
		ExposedPorts map[string]json.RawMessage `json:"exposed_ports"`
	}
	if err := json.Unmarshal([]byte(value), &neg); err != nil {
		return true
	}
	if len(neg.ExposedPorts) > 0 {
		return true
	}
	return neg.Ingress && slices.ContainsFunc(ingresses, func(ing networkingv1.Ingress) bool {
		return servedByGKE(ing) && backsOnto(ing, svc.Name)
	})
}

// servedByGKE reports whether GKE's ingress controller serves an Ingress:
// one whose kubernetes.io/ingress.class names a GKE class, or one with no
// class at all. GKE reads only the annotation, and leaves alone an Ingress
// that sets ingressClassName, as one created where another controller's
// IngressClass is the default has set for it.
func servedByGKE(ing networkingv1.Ingress) bool {
	if class := ing.Annotations[annotationIngressClass]; class != "" {
		return gkeIngressClasses[class]
	}
	return ing.Spec.IngressClassName == nil
}

// backsOnto reports whether an Ingress sends any of its traffic to the
// Service, in its own namespace, by name.
func backsOnto(ing networkingv1.Ingress, service string) bool {
	uses := func(b *networkingv1.IngressBackend) bool {
		return b != nil && b.Service != nil && b.Service.Name == service
	}
	if uses(ing.Spec.DefaultBackend) {
		return true
	}
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for i := range rule.HTTP.Paths {
			if uses(&rule.HTTP.Paths[i].Backend) {
				return true
			}
		}
	}
	return false
}
