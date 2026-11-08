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

package doorman

import (
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// TrimManagedWorkload is how the doorman's cache holds a ManagedWorkload,
// and returns anything else as it is. The doorman needs every
// ManagedWorkload in the namespaces it serves, since their status holds its
// routes, but reads little of each: its name and version, the request last
// stamped on it and the count of those turned away, its wake settings, and
// of its status, the phase, when that last changed, its routes, and when
// its pause began, when it scaled the workload to zero and the stamp it
// recorded. That's all it keeps, without the forecast's state, the costs and
// the conditions that are most of a ManagedWorkload's size. It's safe only
// because the doorman patches a ManagedWorkload, and only its annotations:
// an update would write the rest away.
func TrimManagedWorkload(in any) (any, error) {
	w, ok := in.(*v1alpha1.ManagedWorkload)
	if !ok {
		return in, nil
	}
	out := &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:            w.Name,
			Namespace:       w.Namespace,
			UID:             w.UID,
			ResourceVersion: w.ResourceVersion,
			Annotations:     only(w.Annotations, v1alpha1.AnnotationLastRequest, v1alpha1.AnnotationTurnedAway),
		},
		Spec: v1alpha1.ManagedWorkloadSpec{Wake: w.Spec.Wake},
		Status: v1alpha1.ManagedWorkloadStatus{
			Phase:              w.Status.Phase,
			LastTransitionTime: w.Status.LastTransitionTime,
			Doorman:            w.Status.Doorman,
		},
	}
	if p := w.Status.Pause; p != nil {
		out.Status.Pause = &v1alpha1.PauseStatus{StartedAt: p.StartedAt, PausedAt: p.PausedAt}
		if p.WakeAnnotations != nil {
			out.Status.Pause.WakeAnnotations = &v1alpha1.WakeAnnotations{
				Workload: only(p.WakeAnnotations.Workload, v1alpha1.AnnotationLastRequest),
			}
		}
	}
	return out, nil
}

// only is the entries of m with the keys given, or nil if it has none.
func only(m map[string]string, keys ...string) map[string]string {
	var out map[string]string
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if out == nil {
				out = make(map[string]string, len(keys))
			}
			out[k] = v
		}
	}
	return out
}

// EndpointSliceCache is the doorman's cache of EndpointSlices. It watches
// them so a held connection is passed on the moment its workload has a
// Ready pod, and it can't know in advance which Services that will be, so
// it caches every slice in the namespaces it serves. To keep that small, it
// holds only the slices it trusts, those the EndpointSlice controller wrote
// (see trusted), cut down to the fields it reads: a slice's ports, and
// each endpoint's first address, readiness and pod reference.
func EndpointSliceCache() cache.ByObject {
	return cache.ByObject{
		Label:     labels.SelectorFromSet(labels.Set{discoveryv1.LabelManagedBy: endpointSliceController}),
		Transform: trimEndpointSlice,
	}
}

func trimEndpointSlice(in any) (any, error) {
	slice, ok := in.(*discoveryv1.EndpointSlice)
	if !ok {
		return in, nil
	}
	out := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:            slice.Name,
			Namespace:       slice.Namespace,
			ResourceVersion: slice.ResourceVersion,
			Labels: map[string]string{
				discoveryv1.LabelServiceName: slice.Labels[discoveryv1.LabelServiceName],
				discoveryv1.LabelManagedBy:   slice.Labels[discoveryv1.LabelManagedBy],
			},
		},
		AddressType: slice.AddressType,
		Ports:       slice.Ports,
		Endpoints:   make([]discoveryv1.Endpoint, 0, len(slice.Endpoints)),
	}
	for _, ep := range slice.Endpoints {
		trimmed := discoveryv1.Endpoint{Conditions: discoveryv1.EndpointConditions{
			Ready:       ep.Conditions.Ready,
			Terminating: ep.Conditions.Terminating,
		}}
		if len(ep.Addresses) > 0 {
			trimmed.Addresses = []string{ep.Addresses[0]}
		}
		if ep.TargetRef != nil {
			trimmed.TargetRef = &corev1.ObjectReference{
				Kind:      ep.TargetRef.Kind,
				Namespace: ep.TargetRef.Namespace,
				Name:      ep.TargetRef.Name,
			}
		}
		out.Endpoints = append(out.Endpoints, trimmed)
	}
	return out, nil
}
