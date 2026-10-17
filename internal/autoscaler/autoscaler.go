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

// Package autoscaler finds the HPA or KEDA ScaledObject that scales a
// workload, and pauses and resumes a KEDA one through KEDA itself.
package autoscaler

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

// Kind is what scales a workload.
type Kind string

const (
	HPA  Kind = "HPA"
	KEDA Kind = "KEDA"
)

// PausedReplicasAnnotation tells KEDA to hold a ScaledObject's target at
// that many replicas and stop scaling it.
const PausedReplicasAnnotation = "autoscaling.keda.sh/paused-replicas"

// keda's defaults when a ScaledObject doesn't set them.
const (
	kedaMinDefault = 0
	kedaMaxDefault = 100
)

var scaledObjects = schema.GroupVersionKind{Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledObjectList"}

// Autoscaler is the HPA or ScaledObject that scales a workload, and the
// range it keeps the replicas in.
type Autoscaler struct {
	Kind     Kind
	Name     string
	Min, Max int32

	// PausedReplicas is a ScaledObject's paused-replicas annotation, and nil
	// when it has none.
	PausedReplicas *string
}

// Clamp is n within the autoscaler's range.
func (a Autoscaler) Clamp(n int32) int32 {
	return min(max(n, a.Min), a.Max)
}

// HeldAt reports whether a ScaledObject's paused-replicas annotation already
// holds its target at n replicas.
func (a Autoscaler) HeldAt(n int32) bool {
	return a.PausedReplicas != nil && *a.PausedReplicas == strconv.Itoa(int(n))
}

// kedaRecheck is how long Find remembers that KEDA isn't installed. The
// client asks the API server for all its API groups each time it looks up a
// kind it doesn't know, which on every reconcile of every workload adds up.
const kedaRecheck = 10 * time.Minute

// scaledObjectsReuse is how long Find reuses the ScaledObjects it listed in
// a namespace. ScaledObjects are read unstructured, which the manager's
// cache doesn't serve, so each list goes to the API server, and Find runs
// on every reconcile of every workload. What it finds only reports what
// scales a workload, which can lag by this much. What it saves are the
// lists for a namespace's workloads reconciled together, as a change there
// requeues them all.
const scaledObjectsReuse = 15 * time.Second

// Finder finds what scales a workload.
type Finder struct {
	c   client.Reader
	now func() time.Time

	mu          sync.Mutex
	noKEDAUntil time.Time
	listed      map[string]scaledObjectList
}

// scaledObjectList is a namespace's ScaledObjects as listed at a time.
type scaledObjectList struct {
	items []unstructured.Unstructured
	at    time.Time
}

// NewFinder returns a Finder that reads through c.
func NewFinder(c client.Reader) *Finder {
	return &Finder{c: c, now: time.Now, listed: map[string]scaledObjectList{}}
}

// Find is what scales the workload, and false when nothing does, relying
// on what it last learned of whether KEDA is installed, and on the
// namespace's ScaledObjects as it listed them up to scaledObjectsReuse ago.
// A KEDA ScaledObject also creates an HPA for its target, so it's looked for
// first.
func (f *Finder) Find(ctx context.Context, namespace string, kind v1alpha1.TargetKind, name string) (
	Autoscaler, bool, error) {
	return f.find(ctx, namespace, kind, name, false)
}

// FindNow is Find, but lists the ScaledObjects afresh whatever Find
// learned: for a pause or a resume, which mustn't miss a KEDA installed
// since, or a ScaledObject created or changed. It's rare enough that asking
// the API server again costs nothing.
func (f *Finder) FindNow(ctx context.Context, namespace string, kind v1alpha1.TargetKind, name string) (
	Autoscaler, bool, error) {
	return f.find(ctx, namespace, kind, name, true)
}

func (f *Finder) find(ctx context.Context, namespace string, kind v1alpha1.TargetKind, name string, now bool) (
	Autoscaler, bool, error) {
	so, found, err := f.findScaledObject(ctx, namespace, kind, name, now)
	if err != nil || found {
		return so, found, err
	}
	c := f.c
	var hpas autoscalingv2.HorizontalPodAutoscalerList
	if err := c.List(ctx, &hpas, client.InNamespace(namespace)); err != nil {
		return Autoscaler{}, false, fmt.Errorf("listing HPAs in %s: %w", namespace, err)
	}
	for _, h := range hpas.Items {
		ref := h.Spec.ScaleTargetRef
		if ref.Kind != string(kind) || ref.Name != name {
			continue
		}
		minReplicas := int32(1)
		if h.Spec.MinReplicas != nil {
			minReplicas = *h.Spec.MinReplicas
		}
		return Autoscaler{Kind: HPA, Name: h.Name, Min: minReplicas, Max: h.Spec.MaxReplicas}, true, nil
	}
	return Autoscaler{}, false, nil
}

func (f *Finder) findScaledObject(ctx context.Context, namespace string, kind v1alpha1.TargetKind,
	name string, now bool) (Autoscaler, bool, error) {
	items, err := f.scaledObjects(ctx, namespace, now)
	if err != nil {
		return Autoscaler{}, false, err
	}
	for _, so := range items {
		targetName, _, _ := unstructured.NestedString(so.Object, "spec", "scaleTargetRef", "name")
		targetKind, _, _ := unstructured.NestedString(so.Object, "spec", "scaleTargetRef", "kind")
		if targetKind == "" {
			targetKind = string(v1alpha1.TargetKindDeployment)
		}
		if targetName != name || targetKind != string(kind) {
			continue
		}
		a := Autoscaler{Kind: KEDA, Name: so.GetName(), Min: kedaMinDefault, Max: kedaMaxDefault}
		if v, ok, _ := unstructured.NestedInt64(so.Object, "spec", "minReplicaCount"); ok {
			a.Min = int32(v)
		}
		if v, ok, _ := unstructured.NestedInt64(so.Object, "spec", "maxReplicaCount"); ok {
			a.Max = int32(v)
		}
		if v, ok := so.GetAnnotations()[PausedReplicasAnnotation]; ok {
			a.PausedReplicas = &v
		}
		return a, true, nil
	}
	return Autoscaler{}, false, nil
}

// scaledObjects lists the namespace's ScaledObjects, none when KEDA isn't
// installed. Unless now, it reuses a list made in the last
// scaledObjectsReuse, and relies on KEDA's absence for kedaRecheck.
func (f *Finder) scaledObjects(ctx context.Context, namespace string, now bool) ([]unstructured.Unstructured, error) {
	f.mu.Lock()
	at := f.now()
	if !now {
		if at.Before(f.noKEDAUntil) {
			f.mu.Unlock()
			return nil, nil
		}
		if l, ok := f.listed[namespace]; ok && at.Sub(l.at) < scaledObjectsReuse {
			f.mu.Unlock()
			return l.items, nil
		}
	}
	f.mu.Unlock()

	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(scaledObjects)
	err := f.c.List(ctx, &list, client.InNamespace(namespace))
	if err != nil && !kedaMissing(err) {
		return nil, fmt.Errorf("listing KEDA ScaledObjects in %s: %w", namespace, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.noKEDAUntil = at.Add(kedaRecheck)
		clear(f.listed)
		return nil, nil
	}
	f.noKEDAUntil = time.Time{}
	for ns, l := range f.listed {
		if at.Sub(l.at) >= scaledObjectsReuse {
			delete(f.listed, ns)
		}
	}
	f.listed[namespace] = scaledObjectList{items: list.Items, at: at}
	return list.Items, nil
}

// HoldKEDA has KEDA hold a ScaledObject's target at replicas and stop
// scaling it, so it doesn't undo a pause, or a resume still under way.
func HoldKEDA(ctx context.Context, c client.Client, namespace, name string, replicas int32) error {
	return setPausedReplicas(ctx, c, namespace, name, strconv.Itoa(int(replicas)))
}

// ReleaseKEDA lets KEDA scale a ScaledObject's target again, putting back
// the paused-replicas annotation it had before Hybernate held it, if any.
func ReleaseKEDA(ctx context.Context, c client.Client, namespace, name string, previous *string) error {
	if previous != nil {
		return setPausedReplicas(ctx, c, namespace, name, *previous)
	}
	return setPausedReplicas(ctx, c, namespace, name, nil)
}

// setPausedReplicas sets the annotation, or removes it for a nil value. A
// ScaledObject that's gone, or KEDA uninstalled, leaves nothing to hold.
func setPausedReplicas(ctx context.Context, c client.Client, namespace, name string, value any) error {
	so := &unstructured.Unstructured{}
	so.SetGroupVersionKind(scaledObjects.GroupVersion().WithKind("ScaledObject"))
	so.SetNamespace(namespace)
	so.SetName(name)
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations": map[string]any{PausedReplicasAnnotation: value}}})
	if err != nil {
		return fmt.Errorf("building the ScaledObject patch: %w", err)
	}
	err = c.Patch(ctx, so, client.RawPatch(types.MergePatchType, body))
	if err == nil || kedaMissing(err) {
		return nil
	}
	return fmt.Errorf("annotating ScaledObject %s/%s: %w", namespace, name, err)
}

// kedaMissing reports whether err says there's no ScaledObject to be had.
// KEDA never installed gives no match for the kind; KEDA uninstalled while
// the operator runs gives NotFound instead, because the client's RESTMapper
// still maps the kind it saw, and the API server no longer serves it.
func kedaMissing(err error) bool {
	return meta.IsNoMatchError(err) || apierrors.IsNotFound(err)
}
