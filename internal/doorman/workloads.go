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
	"context"
	"fmt"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// portRoute is one of a workload's routes, and the doorman port it's on.
type portRoute struct {
	port  int32
	route route
}

// routesOf is the routes w's status gives the doorman. w is only read: it
// may be the cache's own.
func routesOf(w *v1alpha1.ManagedWorkload) []portRoute {
	if len(w.Status.Doorman) == 0 {
		return nil
	}
	maxWait := defaultMaxWait
	if w.Spec.Wake != nil && w.Spec.Wake.MaxWait != nil && w.Spec.Wake.MaxWait.Duration > 0 {
		maxWait = min(w.Spec.Wake.MaxWait.Duration, v1alpha1.WakeMaxWaitLimit)
	}
	page := w.Spec.Wake == nil || w.Spec.Wake.Page == nil || *w.Spec.Wake.Page
	out := make([]portRoute, 0, len(w.Status.Doorman))
	for _, r := range w.Status.Doorman {
		out = append(out, portRoute{port: r.DoormanPort, route: route{
			workload: types.NamespacedName{Namespace: w.Namespace, Name: w.Name},
			service:  r.Service,
			portName: r.PortName,
			maxWait:  maxWait,
			page:     page,
			asks:     w.Spec.Wake.Asks(),
			uid:      w.UID,
			began:    pauseBegan(w),
			pausedAt: pausedAt(w),
			waking:   isWaking(w),
			pausing:  w.Status.Phase == v1alpha1.PhasePausing,
			awake:    awake(w),
		}})
	}
	return out
}

// watchedRoutes is the routes of every ManagedWorkload, kept current by the
// events of the cache's informer for them, so a sync of the listeners
// needn't read every ManagedWorkload: a List every RouteSyncInterval
// copies each into the list it fills, about 1.8 KB a ManagedWorkload each
// time, whatever it's cut down to in the cache.
type watchedRoutes struct {
	mu    sync.Mutex
	byKey map[types.NamespacedName][]portRoute
	// version counts the changes to the routes, and claims is what they
	// were as of built, for every sync until the next change. Most events
	// are of the operator's status writes, which change no route.
	version, built uint64
	claims         map[int32][]route
	// synced says the informer has passed on every ManagedWorkload it had
	// when the watch began; until it has, the routes are read from a List.
	synced func() bool
}

// watchWorkloads keeps s.watched current from the informer's events.
func (s *Server) watchWorkloads(ctx context.Context) (func(), error) {
	informer, err := s.informers.GetInformer(ctx, &v1alpha1.ManagedWorkload{})
	if err != nil {
		return nil, fmt.Errorf("getting the managedworkload informer: %w", err)
	}
	w := &watchedRoutes{byKey: map[types.NamespacedName][]portRoute{}}
	registration, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    w.set,
		UpdateFunc: func(_, obj any) { w.set(obj) },
		DeleteFunc: w.remove,
	})
	if err != nil {
		return nil, fmt.Errorf("watching managedworkloads: %w", err)
	}
	w.synced = registration.HasSynced
	s.mu.Lock()
	s.watched = w
	s.mu.Unlock()
	return func() { _ = informer.RemoveEventHandler(registration) }, nil // the informer stops with the cache anyway
}

func (w *watchedRoutes) set(obj any) {
	mw, ok := obj.(*v1alpha1.ManagedWorkload)
	if !ok {
		return
	}
	key := client.ObjectKeyFromObject(mw)
	routes := routesOf(mw)
	w.mu.Lock()
	defer w.mu.Unlock()
	if old, ok := w.byKey[key]; !ok || !slices.Equal(old, routes) {
		w.version++
	}
	w.byKey[key] = routes
}

func (w *watchedRoutes) remove(obj any) {
	if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	mw, ok := obj.(*v1alpha1.ManagedWorkload)
	if !ok {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.byKey, client.ObjectKeyFromObject(mw))
	w.version++
}

// routeClaims is every route, by the port it claims. The map is shared
// with later syncs until a route changes, so it's only read.
func (w *watchedRoutes) routeClaims() map[int32][]route {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.claims != nil && w.built == w.version {
		return w.claims
	}
	claims := map[int32][]route{}
	for _, routes := range w.byKey {
		for _, pr := range routes {
			claims[pr.port] = append(claims[pr.port], pr.route)
		}
	}
	w.claims, w.built = claims, w.version
	return claims
}

func (w *watchedRoutes) exists(key types.NamespacedName) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.byKey[key]
	return ok
}

// workloadClaims is every route, by the port it claims, and whether a
// ManagedWorkload exists: from the informer's events once it has passed on
// them all, otherwise from a List.
func (s *Server) workloadClaims(ctx context.Context) (map[int32][]route, func(types.NamespacedName) bool, error) {
	s.mu.Lock()
	w := s.watched
	s.mu.Unlock()
	if w != nil && w.synced() {
		return w.routeClaims(), w.exists, nil
	}
	// The cache's own objects, not copies, which are only read.
	var list v1alpha1.ManagedWorkloadList
	if err := s.client.List(ctx, &list, client.UnsafeDisableDeepCopy); err != nil {
		return nil, nil, fmt.Errorf("listing managed workloads: %w", err)
	}
	claims := map[int32][]route{}
	existing := make(map[types.NamespacedName]bool, len(list.Items))
	for i := range list.Items {
		mw := &list.Items[i]
		existing[client.ObjectKeyFromObject(mw)] = true
		for _, pr := range routesOf(mw) {
			claims[pr.port] = append(claims[pr.port], pr.route)
		}
	}
	return claims, func(key types.NamespacedName) bool { return existing[key] }, nil
}
