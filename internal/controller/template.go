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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// cachedTemplateHash is where a pod template cut down by TrimPodTemplate
// keeps the hash of the whole template it was cut from, which
// podTemplateHash returns, so a deploy is told apart from a pause as
// before. It's only ever on the operator's cached copies, never written.
const cachedTemplateHash = "napwake.dev/cached-template-hash"

// TrimPodTemplate cuts a Deployment's or StatefulSet's pod template down to
// what the operator reads of it, for its cache, where templates are most
// of a cluster's size: its labels, which Services and dependencies are
// matched by; of each container and init container, its name, ports,
// environment, requests, restart policy, whether it has a readiness or
// startup probe, and its CPU limit if it requests no CPU, since the limit
// then stands in for the request; the volumes that are claims; and the
// grace period, readiness gates and pod-level resources. The hash of the
// whole template goes with it. A template already trimmed is left as it
// is.
//
// The operator never updates a Deployment or StatefulSet from its cache:
// it patches them with what it sets alone, and scales them through the
// scale subresource, read fresh. Only its forwarders' Deployments are
// patched from the cache, and those aren't trimmed.
func TrimPodTemplate(template *corev1.PodTemplateSpec) {
	if _, trimmed := template.Annotations[cachedTemplateHash]; trimmed {
		return
	}
	hash := hashTemplate(*template)
	var volumes []corev1.Volume
	for _, v := range template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			volumes = append(volumes, corev1.Volume{Name: v.Name,
				VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: v.PersistentVolumeClaim}})
		}
	}
	*template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: template.Labels, Annotations: map[string]string{cachedTemplateHash: hash}},
		Spec: corev1.PodSpec{
			InitContainers:                trimContainers(template.Spec.InitContainers),
			Containers:                    trimContainers(template.Spec.Containers),
			Volumes:                       volumes,
			TerminationGracePeriodSeconds: template.Spec.TerminationGracePeriodSeconds,
			ReadinessGates:                template.Spec.ReadinessGates,
			Resources:                     template.Spec.Resources,
			Overhead:                      template.Spec.Overhead,
		},
	}
}

func trimContainers(in []corev1.Container) []corev1.Container {
	if in == nil {
		return nil
	}
	out := make([]corev1.Container, len(in))
	for i, c := range in {
		out[i] = corev1.Container{Name: c.Name, Ports: c.Ports, Env: c.Env, EnvFrom: c.EnvFrom,
			Resources: corev1.ResourceRequirements{Requests: c.Resources.Requests}, RestartPolicy: c.RestartPolicy}
		if _, requested := c.Resources.Requests[corev1.ResourceCPU]; !requested {
			if cpu, limited := c.Resources.Limits[corev1.ResourceCPU]; limited {
				out[i].Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: cpu}
			}
		}
		if c.ReadinessProbe != nil {
			out[i].ReadinessProbe = &corev1.Probe{}
		}
		if c.StartupProbe != nil {
			out[i].StartupProbe = &corev1.Probe{}
		}
	}
	return out
}

// hashTemplate fingerprints a whole pod template.
func hashTemplate(template corev1.PodTemplateSpec) string {
	data, err := json.Marshal(template)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
