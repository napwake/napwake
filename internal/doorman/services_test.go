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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The doorman routes a Service's TCP ports, less those doorman-ignore-ports
// lists. Its UDP and SCTP ports it can't route, and those not listed fail
// while the workload is paused.
func TestRoutableAndUnroutablePorts(t *testing.T) {
	svc := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{AnnotationIgnorePorts: "metrics, 5353"}},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Name: "http", Port: 80},
			{Name: "grpc", Port: 9090, Protocol: corev1.ProtocolTCP},
			{Name: "metrics", Port: 9100},
			{Name: "dns", Port: 53, Protocol: corev1.ProtocolUDP},
			{Name: "mdns", Port: 5353, Protocol: corev1.ProtocolUDP},
			{Name: "sctp", Port: 3868, Protocol: corev1.ProtocolSCTP},
		}},
	}
	names := func(ports []corev1.ServicePort) []string {
		out := make([]string, 0, len(ports))
		for _, p := range ports {
			out = append(out, p.Name)
		}
		return out
	}
	assert.Equal(t, []string{"http", "grpc"}, names(RoutablePorts(svc)))
	assert.Equal(t, []string{"dns", "sctp"}, names(UnroutablePorts(svc)), "mdns is ignored on purpose")
}
