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
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// turnedAwayFlush is how often each replica adds the requests it turned
// away to the workload's count, so a flood of them costs one write a
// minute, not one each.
const turnedAwayFlush = time.Minute

// awayCount is what this replica turned away from one workload since its
// last flush, during the pause that began at pausedAt.
type awayCount struct {
	pausedAt time.Time
	requests int64
}

// turnedAway notes a request turned away from rt's workload. Requests from
// an earlier pause not yet flushed are dropped: that pause is over.
func (s *Server) turnedAway(rt route) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.away[rt.workload]
	if !c.pausedAt.Equal(rt.pausedAt) {
		c = awayCount{pausedAt: rt.pausedAt}
	}
	c.requests++
	s.away[rt.workload] = c
}

// watchTurnedAway flushes the requests turned away every turnedAwayFlush
// until ctx is done. Start flushes what's left once its connections are.
func (s *Server) watchTurnedAway(ctx context.Context) {
	ticker := time.NewTicker(s.awayFlush)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.flushTurnedAway(ctx)
	}
}

// flushTurnedAway adds what this replica turned away to each workload's
// count. A count that can't be written is kept for the next flush.
func (s *Server) flushTurnedAway(ctx context.Context) {
	s.mu.Lock()
	pending := s.away
	s.away = map[types.NamespacedName]awayCount{}
	s.mu.Unlock()
	for key, c := range pending {
		if err := s.addTurnedAway(ctx, key, c); err != nil {
			log.FromContext(ctx).WithName("doorman").Error(err, "counting requests turned away",
				"workload", key.Name, "namespace", key.Namespace)
			s.mu.Lock()
			if now := s.away[key]; now.pausedAt.Equal(c.pausedAt) {
				c.requests += now.requests
			}
			s.away[key] = c
			s.mu.Unlock()
		}
	}
}

// addTurnedAway adds c to the workload's count in its turned-away
// annotation, which other replicas add to as well, so each write is locked
// to the version it read.
func (s *Server) addTurnedAway(ctx context.Context, key types.NamespacedName, c awayCount) error {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	for range stampAttempts {
		var w v1alpha1.ManagedWorkload
		if err := s.client.Get(ctx, key, &w); err != nil {
			return client.IgnoreNotFound(err)
		}
		count, ok := withTurnedAway(w.Annotations[v1alpha1.AnnotationTurnedAway], c, pausedAt(&w))
		if !ok {
			return nil
		}
		value, err := json.Marshal(count)
		if err != nil {
			return fmt.Errorf("encoding the count: %w", err)
		}
		original := w.DeepCopy()
		if w.Annotations == nil {
			w.Annotations = map[string]string{}
		}
		w.Annotations[v1alpha1.AnnotationTurnedAway] = string(value)
		err = s.client.Patch(ctx, &w, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}))
		if !apierrors.IsConflict(err) {
			return client.IgnoreNotFound(err)
		}
	}
	return fmt.Errorf("the workload kept changing while its count was written")
}

// withTurnedAway is the count after adding c to current, the annotation's
// value, for a workload whose current pause began at latest, or zero while
// it's awake: c's pause's count goes on; an earlier pause's, one that can't
// be read, one without a pause, or one of a pause after latest, as a count
// edited by hand might be, starts again from c; and ok is false when the
// count is already of a later pause than c's, or c names no pause.
func withTurnedAway(current string, c awayCount, latest time.Time) (v1alpha1.TurnedAway, bool) {
	if c.pausedAt.IsZero() {
		return v1alpha1.TurnedAway{}, false
	}
	count := v1alpha1.TurnedAway{PausedAt: metav1.NewTime(c.pausedAt), Requests: c.requests}
	var have v1alpha1.TurnedAway
	if json.Unmarshal([]byte(current), &have) != nil || have.Requests < 0 || have.PausedAt.IsZero() {
		return count, true
	}
	at := have.PausedAt.Truncate(time.Second)
	if !latest.IsZero() && at.After(latest) {
		return count, true
	}
	switch {
	case at.Equal(c.pausedAt):
		count.Requests += have.Requests
	case at.After(c.pausedAt):
		return v1alpha1.TurnedAway{}, false
	}
	return count, true
}
