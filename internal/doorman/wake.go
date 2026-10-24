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
	"errors"
	"fmt"
	"net/netip"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/human"
)

const (
	// wakeStampInterval is how long a stamp this replica made is trusted to
	// be on its way into the cache, so a burst of connections arriving
	// before it shows up doesn't patch the workload once each.
	wakeStampInterval = 10 * time.Second

	// unservedEventInterval keeps a burst of timed-out connections, all
	// waiting on the same slow wake, to one warning event.
	unservedEventInterval = time.Minute

	apiTimeout = 10 * time.Second
)

// errWakeLimited means the caller has started too many wakes lately.
var errWakeLimited = errors.New("too many wakes from this source")

// errNotAsleep means the doorman's cache shows a routed workload awake, as
// it can for a moment around a pause, so it isn't stamped yet. A held
// connection asks again until the cache shows it paused, or a pod is
// Ready.
var errNotAsleep = errors.New("the workload shows as awake")

// wakeCall is one stamp in flight, shared by every connection that needs
// the same workload woken at the same time.
type wakeCall struct {
	done   chan struct{}
	err    error
	source netip.Addr
}

// stampRecord is the last stamp this replica made for a workload, and the
// pause it woke, named by when it began, since a pause called off as it
// paused never scaled the workload to zero.
type stampRecord struct {
	began time.Time
	at    time.Time
}

// requestWake makes sure the workload is woken: it stamps the last-request
// annotation unless a wake is already on its way. Connections for the same
// workload share one stamp. A failed stamp is returned, for the caller to
// retry while it holds the connection.
//
// The stamp is for every connection waiting on it, not only the one that
// made it, so it's bound by ctx, the doorman's connections' context, which
// ends only when shutdown closes them all. waitCtx bounds how long this
// caller waits for another's stamp.
func (s *Server) requestWake(ctx, waitCtx context.Context, rt route, from netip.Addr) error {
	for {
		s.mu.Lock()
		call, inFlight := s.wakeCalls[rt.workload]
		if !inFlight {
			call = &wakeCall{done: make(chan struct{}), source: from}
			s.wakeCalls[rt.workload] = call
		}
		s.mu.Unlock()

		if inFlight {
			select {
			case <-call.done:
			case <-waitCtx.Done():
				return waitCtx.Err()
			}
			// Another source's allowance says nothing about this one's.
			if errors.Is(call.err, errWakeLimited) && call.source != from {
				continue
			}
			return call.err
		}

		call.err = s.stamp(ctx, rt, from)
		s.mu.Lock()
		delete(s.wakeCalls, rt.workload)
		s.mu.Unlock()
		close(call.done)
		if call.err != nil && !errors.Is(call.err, errWakeLimited) && !errors.Is(call.err, errClickLimited) &&
			!errors.Is(call.err, errNotAsleep) {
			log.FromContext(ctx).Error(call.err, "waking workload",
				"workload", rt.workload.Name, "namespace", rt.workload.Namespace, "service", rt.service)
		}
		return call.err
	}
}

// stamp writes a new last-request value, and where the request came from, so
// the operator can learn what depends on the workload. The value always
// differs from the one recorded when the pause began, which is what the
// operator takes as a wake.
func (s *Server) stamp(ctx context.Context, rt route, from netip.Addr) error {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	var w v1alpha1.ManagedWorkload
	if err := s.client.Get(ctx, rt.workload, &w); err != nil {
		return fmt.Errorf("getting managed workload: %w", err)
	}
	if !needsWake(&w) {
		if awake(&w) {
			return errNotAsleep
		}
		return nil
	}
	now := s.now()
	began := pauseBegan(&w)
	s.mu.Lock()
	last, stamped := s.stamped[rt.workload]
	s.mu.Unlock()
	if stamped && last.began.Equal(began) && now.Sub(last.at) < wakeStampInterval {
		return nil
	}
	if rt.asks && stampedRecently(&w, now) {
		return errClickLimited
	}
	if !s.admission.allowWake(from, now) {
		return errWakeLimited
	}

	won, err := s.patchStamp(ctx, &w, from, now)
	if err != nil {
		return fmt.Errorf("stamping last request: %w", err)
	}

	s.mu.Lock()
	s.stamped[rt.workload] = stampRecord{began: began, at: now}
	s.mu.Unlock()
	// A stamp while the workload pauses calls the pause off, which the
	// operator's PauseCancelled announces.
	if s.recorder != nil && won && w.Status.Phase == v1alpha1.PhasePaused {
		what := "request"
		if rt.asks {
			what = "a person asked"
		}
		s.recorder.Eventf(&w, nil, "Normal", "WokenByRequest", "Wake", "%s on Service %s, waking", what, rt.service)
	}
	return nil
}

// stampAttempts is how many times patchStamp writes the stamp, the last of
// them unlocked.
const stampAttempts = 3

// patchStamp writes the stamp to w, and reports whether it was this
// replica's to announce.
//
// Each write is locked to the version last read, so when two replicas stamp
// at once, only the first to land announces the wake. On a conflict, the
// workload is read again: one another replica has stamped since needs
// nothing more, and one changed otherwise, as the operator's status writes
// change it, is stamped again. The last write is unlocked, so the wake is
// never lost, and announces nothing, in case it was another replica's.
func (s *Server) patchStamp(ctx context.Context, w *v1alpha1.ManagedWorkload, from netip.Addr, now time.Time) (bool, error) {
	for attempt := 1; ; attempt++ {
		original := w.DeepCopy()
		setStamp(w, from, now)
		if attempt == stampAttempts {
			return false, s.client.Patch(ctx, w, client.MergeFrom(original))
		}
		err := s.client.Patch(ctx, w, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}))
		if !apierrors.IsConflict(err) {
			return err == nil, err
		}
		var fresh v1alpha1.ManagedWorkload
		if err := s.client.Get(ctx, client.ObjectKeyFromObject(w), &fresh); err != nil {
			return false, fmt.Errorf("getting managed workload: %w", err)
		}
		*w = fresh
		if !needsWake(w) {
			return false, nil
		}
	}
}

// setStamp sets the last-request annotations on w.
func setStamp(w *v1alpha1.ManagedWorkload, from netip.Addr, now time.Time) {
	if w.Annotations == nil {
		w.Annotations = map[string]string{}
	}
	value := now.UTC().Format(time.RFC3339)
	if value == w.Annotations[v1alpha1.AnnotationLastRequest] || value == recordedLastRequest(w) {
		value = now.UTC().Format(time.RFC3339Nano)
	}
	w.Annotations[v1alpha1.AnnotationLastRequest] = value
	w.Annotations[v1alpha1.AnnotationLastRequestFrom] = from.Unmap().String()
}

// needsWake reports whether a paused workload, or one pausing, has no wake
// pending. The operator takes a last-request value as a wake when it's set
// and differs from the one recorded when the pause began, so one that's
// empty, or removed since, is no wake. A stamp while it's pausing ends the
// pause. A workload in any other phase is awake, or already waking.
func needsWake(w *v1alpha1.ManagedWorkload) bool {
	if !asleep(w) {
		return false
	}
	current := w.Annotations[v1alpha1.AnnotationLastRequest]
	if current == "" {
		return true
	}
	pause := w.Status.Pause
	if pause != nil && pause.WakeAnnotations != nil {
		return current == recordedLastRequest(w)
	}
	stamped, err := time.Parse(time.RFC3339, current)
	if err != nil || pause == nil || pause.PausedAt == nil {
		return true
	}
	return stamped.Before(pause.PausedAt.Time)
}

// asleep reports whether the workload is paused, or pausing, which the
// doorman fronts it for from the moment the pause starts.
func asleep(w *v1alpha1.ManagedWorkload) bool {
	return w.Status.Phase == v1alpha1.PhasePaused || w.Status.Phase == v1alpha1.PhasePausing
}

// awake reports whether the workload's phase shows it awake: neither
// asleep nor waking.
func awake(w *v1alpha1.ManagedWorkload) bool {
	return !asleep(w) && w.Status.Phase != v1alpha1.PhaseResuming
}

// recordedLastRequest is the last-request value the pause recorded.
func recordedLastRequest(w *v1alpha1.ManagedWorkload) string {
	if p := w.Status.Pause; p != nil && p.WakeAnnotations != nil {
		return p.WakeAnnotations.Workload[v1alpha1.AnnotationLastRequest]
	}
	return ""
}

// pausedAt is when the workload's pause scaled it to zero, to the second,
// as status records it, so every replica names a pause the same.
func pausedAt(w *v1alpha1.ManagedWorkload) time.Time {
	if p := w.Status.Pause; p != nil && p.PausedAt != nil {
		return p.PausedAt.Truncate(time.Second).UTC()
	}
	return time.Time{}
}

// pauseBegan is when the workload's pause began, as it went Pausing, to
// the second, which names the pause from its first moment. A pause begun by
// an operator from before startedAt is named by when it scaled the workload
// to zero. Zero for a workload with no pause recorded.
func pauseBegan(w *v1alpha1.ManagedWorkload) time.Time {
	if p := w.Status.Pause; p != nil && p.StartedAt != nil {
		return p.StartedAt.Truncate(time.Second).UTC()
	}
	return pausedAt(w)
}

// warnUnserved emits a warning event on the workload for a held connection
// that was closed without reaching it, at most once per unservedEventInterval.
// The metrics count every one; the event is what shows in kubectl describe.
func (s *Server) warnUnserved(ctx context.Context, rt route, result string, waited time.Duration) {
	if s.recorder == nil {
		return
	}
	now := s.now()
	s.mu.Lock()
	if last, ok := s.lastWarn[rt.workload]; ok && now.Sub(last) < unservedEventInterval {
		s.mu.Unlock()
		return
	}
	s.lastWarn[rt.workload] = now
	s.mu.Unlock()

	// ctx is the doorman's connections' context, not the connection's,
	// which may be what ran out: shutdown cuts the lookup short only once
	// it closes every connection.
	getCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	var w v1alpha1.ManagedWorkload
	if err := s.client.Get(getCtx, rt.workload, &w); err != nil {
		log.FromContext(ctx).Error(err, "getting managed workload for event",
			"workload", rt.workload.Name, "namespace", rt.workload.Namespace)
		return
	}
	reason := "the workload wasn't Ready within maxWait; the wake continues"
	if result != resultTimeout {
		reason = "the workload couldn't be reached"
	}
	s.recorder.Eventf(&w, nil, "Warning", "RequestNotServed", "Wake",
		"a request on Service %s was closed after %s: %s", rt.service, human.Duration(waited), reason)
}
