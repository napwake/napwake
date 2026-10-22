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

package doorman

import (
	"encoding/json"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// AnnotationGKENEGStatus is where GKE's NEG controller lists the network
	// endpoint groups it keeps for a Service, removed once it keeps none.
	AnnotationGKENEGStatus = "cloud.google.com/neg-status"

	// vmIPNEGPort is the port the NEG controller lists a GCE_VM_IP NEG
	// under, which an internal or external passthrough load balancer uses.
	// Those NEGs hold nodes, not pods, and reach the doorman through
	// kube-proxy like any LoadBalancer Service.
	vmIPNEGPort = "0"
)

// RoutablePorts are a Service's TCP ports, less those its
// hybernate.io/doorman-ignore-ports annotation lists by name or number.
// Those fail while the workload is paused, as they would without the
// doorman, which is what a port only health checks or scrapers use wants.
func RoutablePorts(svc corev1.Service) []corev1.ServicePort {
	ignored := ignoredPorts(svc)
	var out []corev1.ServicePort
	for _, port := range svc.Spec.Ports {
		if tcp(port) && !ignored(port) {
			out = append(out, port)
		}
	}
	return out
}

// UnroutablePorts are a Service's ports the doorman can't route, UDP and
// SCTP, less those its hybernate.io/doorman-ignore-ports annotation lists:
// traffic to them fails while the workload is paused, though nobody said it
// may.
func UnroutablePorts(svc corev1.Service) []corev1.ServicePort {
	ignored := ignoredPorts(svc)
	var out []corev1.ServicePort
	for _, port := range svc.Spec.Ports {
		if !tcp(port) && !ignored(port) {
			out = append(out, port)
		}
	}
	return out
}

func tcp(port corev1.ServicePort) bool {
	return port.Protocol == "" || port.Protocol == corev1.ProtocolTCP
}

// ignoredPorts reports whether the Service's hybernate.io/doorman-ignore-ports
// annotation lists a port, by name or number.
func ignoredPorts(svc corev1.Service) func(corev1.ServicePort) bool {
	ignored := map[string]bool{}
	for p := range strings.SplitSeq(svc.Annotations[AnnotationIgnorePorts], ",") {
		if p = strings.TrimSpace(p); p != "" {
			ignored[p] = true
		}
	}
	return func(port corev1.ServicePort) bool {
		return ignored[strconv.Itoa(int(port.Port))] || (port.Name != "" && ignored[port.Name])
	}
}

// InPodNEGs reports whether GKE's NEG controller lists, for the Service, a
// NEG that holds pods. A status that can't be parsed counts, to stay safe.
func InPodNEGs(svc corev1.Service) bool {
	value, ok := svc.Annotations[AnnotationGKENEGStatus]
	if !ok {
		return false
	}
	var status struct {
		NEGs map[string]string `json:"network_endpoint_groups"`
	}
	if err := json.Unmarshal([]byte(value), &status); err != nil {
		return true
	}
	for port := range status.NEGs {
		if port != vmIPNEGPort {
			return true
		}
	}
	return false
}
