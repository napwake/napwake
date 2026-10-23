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

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

var wakeTime = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

var apiKey = client.ObjectKey{Namespace: "preview-42", Name: "api"}

func managedWorkload(phase v1alpha1.WorkloadPhase) *v1alpha1.ManagedWorkload {
	return &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: apiKey.Name, Namespace: apiKey.Namespace},
		Spec: v1alpha1.ManagedWorkloadSpec{
			Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"},
		},
		Status: v1alpha1.ManagedWorkloadStatus{Phase: phase},
	}
}

func testOptions() wakeOptions {
	return wakeOptions{wait: true, pollInterval: time.Millisecond, now: func() time.Time { return wakeTime }}
}

// testContext bounds each wake, so a bug that keeps it waiting fails the
// test instead of hanging until go test's timeout.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newClient(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(funcs).Build()
}

func annotations(t *testing.T, c client.Client) map[string]string {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, c.Get(context.Background(), apiKey, &w))
	return w.Annotations
}

// wakes answers a request the way the operator does, with reason, and has
// the workload Running from the runningFrom'th read of it on.
func wakes(reason string, runningFrom int) interceptor.Funcs {
	reads := 0
	return interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey,
		obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		w, ok := obj.(*v1alpha1.ManagedWorkload)
		if !ok {
			return nil
		}
		if reads++; reads > 1 {
			answerWake(w, metav1.ConditionTrue, reason, "woken as requested at 2026-10-03 09:00 UTC")
		}
		if reads >= runningFrom {
			w.Status.Phase = v1alpha1.PhaseRunning
		}
		return nil
	}}
}

func answerWake(w *v1alpha1.ManagedWorkload, status metav1.ConditionStatus, reason, message string) {
	w.Status.LastWakeRequest = w.Annotations[v1alpha1.AnnotationWakeRequested]
	meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{Type: "WakeRequest", Status: status,
		Reason: reason, Message: message})
}

// Wake asks Hybernate to wake the workload with a token of its own, the
// time of the request, and keeps it awake for --for, unless it's held
// awake longer already.
func TestWake_RequestsAWake(t *testing.T) {
	tests := []struct {
		name            string
		phase           v1alpha1.WorkloadPhase
		keepAwake       time.Duration
		held            string
		wantActiveUntil string
		wantOutput      string
	}{
		{name: "a paused workload", phase: v1alpha1.PhasePaused, wantOutput: "waking preview-42/api\n"},
		{
			name: "kept awake for a while", phase: v1alpha1.PhasePaused, keepAwake: 2 * time.Hour,
			wantActiveUntil: "2026-10-03T11:00:00Z", wantOutput: "waking preview-42/api\n",
		},
		{
			name: "held awake for less", phase: v1alpha1.PhasePaused, keepAwake: 2 * time.Hour,
			held: "2026-10-03T10:00:00Z", wantActiveUntil: "2026-10-03T11:00:00Z", wantOutput: "waking preview-42/api\n",
		},
		{
			name: "held awake for longer", phase: v1alpha1.PhasePaused, keepAwake: 2 * time.Hour,
			held: "2026-10-03T13:00:00Z", wantActiveUntil: "2026-10-03T13:00:00Z",
			wantOutput: "preview-42/api is already held awake until 2026-10-03T13:00:00Z, later than --for asks, so " +
				"that stays\nwaking preview-42/api\n",
		},
		{
			name: "a running workload", phase: v1alpha1.PhaseRunning,
			wantOutput: "preview-42/api is already running; its idle clock restarts now\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := managedWorkload(tt.phase)
			if tt.held != "" {
				w.Annotations = map[string]string{v1alpha1.AnnotationActiveUntil: tt.held}
			}
			c := newClient(t, interceptor.Funcs{}, w)
			opts := testOptions()
			opts.wait = false
			opts.keepAwake = tt.keepAwake
			var out bytes.Buffer

			require.NoError(t, wake(testContext(t), c, apiKey, opts, &out))

			got := annotations(t, c)
			assert.Equal(t, "2026-10-03T09:00:00Z", got[v1alpha1.AnnotationWakeRequested])
			assert.NotContains(t, got, v1alpha1.AnnotationLastActivity, "the request is what wakes it")
			assert.Equal(t, tt.wantActiveUntil, got[v1alpha1.AnnotationActiveUntil])
			assert.Equal(t, tt.wantOutput, out.String())
		})
	}
}

// Each request gets a token of its own, so Hybernate acts on a wake asked
// for again after the last one.
func TestWake_EachRequestIsNew(t *testing.T) {
	w := managedWorkload(v1alpha1.PhasePaused)
	w.Annotations = map[string]string{v1alpha1.AnnotationWakeRequested: "2026-10-01T18:00:00Z"}
	w.Status.LastWakeRequest = "2026-10-01T18:00:00Z"
	c := newClient(t, interceptor.Funcs{}, w)
	opts := testOptions()
	opts.wait = false

	require.NoError(t, wake(testContext(t), c, apiKey, opts, &bytes.Buffer{}))

	assert.Equal(t, "2026-10-03T09:00:00Z", annotations(t, c)[v1alpha1.AnnotationWakeRequested])
}

// Wake waits for Hybernate's answer to its request, and then until the
// workload is Running: a phase Running from before the answer, as the cache
// can show, isn't taken for the wake.
func TestWake_WaitsForTheAnswerThenRunning(t *testing.T) {
	var gets int
	answersLate := func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
		opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		w, ok := obj.(*v1alpha1.ManagedWorkload)
		if !ok {
			return nil
		}
		gets++
		switch {
		case gets >= 5:
			w.Status.Phase = v1alpha1.PhaseRunning
			answerWake(w, metav1.ConditionTrue, "Woken", "woken as requested at 2026-10-03 09:00 UTC")
		case gets >= 3:
			w.Status.Phase = v1alpha1.PhaseRunning
		}
		return nil
	}
	c := newClient(t, interceptor.Funcs{Get: answersLate}, managedWorkload(v1alpha1.PhasePaused))
	var out bytes.Buffer

	require.NoError(t, wake(testContext(t), c, apiKey, testOptions(), &out))

	assert.Equal(t, "waking preview-42/api...\npreview-42/api is Running after 0s\n", out.String())
	assert.GreaterOrEqual(t, gets, 5, "it waits for the answer")
}

// A workload that's awake is answered as such, and wake says so once it is.
func TestWake_AlreadyAwake(t *testing.T) {
	c := newClient(t, wakes("AlreadyAwake", 0), managedWorkload(v1alpha1.PhaseRunning))
	var out bytes.Buffer

	require.NoError(t, wake(testContext(t), c, apiKey, testOptions(), &out))

	assert.Equal(t, "preview-42/api is already running; its idle clock restarts now\n", out.String())
}

// A request Hybernate won't act on fails the wake with Hybernate's reason,
// rather than waiting out the timeout.
func TestWake_Refused(t *testing.T) {
	refuses := func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
		opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		if w, ok := obj.(*v1alpha1.ManagedWorkload); ok {
			answerWake(w, metav1.ConditionFalse, "ScaledToZero", "not woken at 2026-10-03 09:00 UTC: it's scaled "+
				"to zero outside Hybernate, which leaves it off until its replicas are set above zero")
		}
		return nil
	}
	c := newClient(t, interceptor.Funcs{Get: refuses}, managedWorkload(v1alpha1.PhaseRunning))

	_, err := runCLI(t, c, "wake", "api")

	require.ErrorIs(t, err, errNotWoken)
	assert.EqualError(t, err, "preview-42/api wasn't woken: it's scaled to zero outside Hybernate, which leaves it "+
		"off until its replicas are set above zero")
}

// laterRequest stands in for another tool asking too, just after wake: its
// token replaces wake's on the annotation, and Hybernate answers it,
// leaving the workload in phase.
func laterRequest(status metav1.ConditionStatus, reason, message string,
	phase v1alpha1.WorkloadPhase) interceptor.Funcs {
	reads := 0
	return interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey,
		obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		w, ok := obj.(*v1alpha1.ManagedWorkload)
		if !ok {
			return nil
		}
		if reads++; reads > 1 {
			w.Annotations[v1alpha1.AnnotationWakeRequested] = "the-bots-token"
			answerWake(w, status, reason, message)
			w.Status.Phase = phase
		}
		return nil
	}}
}

// Another tool asking just after wake replaces its token, and Hybernate
// answers that one: what came of it is what came of wake's, so wake doesn't
// wait out its timeout for an answer that can't come.
func TestWake_AnsweredThroughALaterRequest(t *testing.T) {
	t.Run("woken", func(t *testing.T) {
		w := managedWorkload(v1alpha1.PhasePaused)
		w.Annotations = map[string]string{v1alpha1.AnnotationWakeRequested: "an-earlier-request"}
		w.Status.LastWakeRequest = "an-earlier-request"
		c := newClient(t, laterRequest(metav1.ConditionTrue, "Woken", "woken as requested at 2026-10-03 09:00 UTC",
			v1alpha1.PhaseRunning), w)
		var out bytes.Buffer

		require.NoError(t, wake(testContext(t), c, apiKey, testOptions(), &out))

		assert.Equal(t, "waking preview-42/api...\npreview-42/api is Running after 0s\n", out.String())
	})
	t.Run("refused", func(t *testing.T) {
		c := newClient(t, laterRequest(metav1.ConditionFalse, "ScaledToZero", "not woken at 2026-10-03 09:00 UTC: "+
			"it's scaled to zero outside Hybernate, which leaves it off until its replicas are set above zero",
			v1alpha1.PhaseRunning), managedWorkload(v1alpha1.PhasePaused))

		err := wake(testContext(t), c, apiKey, testOptions(), &bytes.Buffer{})

		require.ErrorIs(t, err, errNotWoken)
		assert.Contains(t, err.Error(), "it's scaled to zero outside Hybernate")
	})
	t.Run("not yet Running", func(t *testing.T) {
		c := newClient(t, laterRequest(metav1.ConditionTrue, "Woken", "woken as requested at 2026-10-03 09:00 UTC",
			v1alpha1.PhaseResuming), managedWorkload(v1alpha1.PhasePaused))
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		err := wake(ctx, c, apiKey, testOptions(), &bytes.Buffer{})

		require.ErrorIs(t, err, errNotRunningYet)
		assert.Contains(t, err.Error(), "it's still Resuming (", "taken up, through the later request")
	})
}

// A workload read as Running while a pause was already under way is
// paused, then woken by the request: wake waits until it's Running again,
// rather than return as the wake begins, saying it was already running.
func TestWake_WokenAfterAPauseUnderWay(t *testing.T) {
	reads := 0
	funcs := interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey,
		obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		w, ok := obj.(*v1alpha1.ManagedWorkload)
		if !ok {
			return nil
		}
		reads++
		switch {
		case reads == 1:
			w.Status.Phase = v1alpha1.PhaseRunning
		case reads == 2:
			w.Status.Phase = v1alpha1.PhasePausing
		case reads == 3:
			w.Status.Phase = v1alpha1.PhasePaused
		case reads < 6:
			w.Status.Phase = v1alpha1.PhaseResuming
			answerWake(w, metav1.ConditionTrue, "Woken", "woken as requested at 2026-10-03 09:00 UTC")
		default:
			w.Status.Phase = v1alpha1.PhaseRunning
			answerWake(w, metav1.ConditionTrue, "Woken", "woken as requested at 2026-10-03 09:00 UTC")
		}
		return nil
	}}
	c := newClient(t, funcs, managedWorkload(v1alpha1.PhaseRunning))
	var out bytes.Buffer

	require.NoError(t, wake(testContext(t), c, apiKey, testOptions(), &out))

	assert.Equal(t, "preview-42/api is Running after 0s\n", out.String())
	assert.GreaterOrEqual(t, reads, 6, "it waits until the workload is Running")
}

// A ManagedWorkload just created, which Hybernate hasn't taken on yet, has
// no status: Hybernate takes it on Running and answers the request as for
// any awake workload, and wake says so.
func TestWake_AManagedWorkloadJustCreated(t *testing.T) {
	c := newClient(t, wakes("AlreadyAwake", 0), managedWorkload(""))
	var out bytes.Buffer

	require.NoError(t, wake(testContext(t), c, apiKey, testOptions(), &out))

	assert.Equal(t, "preview-42/api is already running; its idle clock restarts now\n", out.String())
}

// A ManagedWorkload deleted while wake waits has handed its workload back.
func TestWake_DeletedWhileWaiting(t *testing.T) {
	var gets int
	deleted := func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
		opts ...client.GetOption) error {
		if gets++; gets > 1 {
			return apierrors.NewNotFound(schema.GroupResource{Group: "hybernate.io", Resource: "managedworkloads"},
				key.Name)
		}
		return c.Get(ctx, key, obj, opts...)
	}
	c := newClient(t, interceptor.Funcs{Get: deleted}, managedWorkload(v1alpha1.PhasePaused))

	err := wake(testContext(t), c, apiKey, testOptions(), &bytes.Buffer{})

	require.ErrorIs(t, err, errNotWoken)
	assert.Contains(t, err.Error(), "its ManagedWorkload api was deleted")
}

// Hybernate not answering, as when its operator isn't running, is said so
// when the wait runs out.
func TestWake_TimesOut(t *testing.T) {
	c := newClient(t, interceptor.Funcs{}, managedWorkload(v1alpha1.PhasePaused))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := wake(ctx, c, apiKey, testOptions(), &bytes.Buffer{})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "still Paused, and Hybernate hasn't taken up the wake request")
	assert.Contains(t, err.Error(), "kubectl describe managedworkload api -n preview-42")
}

// slowReads stands in for an API server whose answers, after the first,
// take until the command's deadline, as a real client then fails them.
func slowReads() interceptor.Funcs {
	reads := 0
	return interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey,
		obj client.Object, opts ...client.GetOption) error {
		if reads++; reads > 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		return c.Get(ctx, key, obj, opts...)
	}}
}

// The wait running out during a poll's read is the workload not being
// Running yet, with the phase last seen, not the API server failing to
// answer.
func TestWake_TimesOutDuringARead(t *testing.T) {
	c := newClient(t, slowReads(), managedWorkload(v1alpha1.PhasePaused))

	_, err := runCLI(t, c, "wake", "api", "--timeout", "100ms")

	require.ErrorIs(t, err, errNotRunningYet)
	assert.Contains(t, err.Error(), "preview-42/api isn't Running yet: it's still Paused")
	assert.NotContains(t, err.Error(), "API server")
}

// A ManagedWorkload another one's target already belongs to isn't acted on,
// so a wake through it would only wait out the timeout.
func TestWake_RefusesADuplicate(t *testing.T) {
	duplicate := managedWorkload(v1alpha1.PhaseRunning)
	duplicate.Status.Conditions = []metav1.Condition{{Type: "DuplicateTarget", Status: metav1.ConditionTrue,
		Reason: "DuplicateTarget", Message: "Deployment/api is already managed by api-first"}}
	c := newClient(t, interceptor.Funcs{}, duplicate)

	err := wake(testContext(t), c, apiKey, testOptions(), &bytes.Buffer{})

	require.ErrorIs(t, err, errNotWakeable)
	assert.Contains(t, err.Error(), "Deployment/api is already managed by api-first")
	assert.Empty(t, annotations(t, c), "nothing is stamped when it couldn't wake it")
}

func TestWake_NotFound(t *testing.T) {
	c := newClient(t, interceptor.Funcs{})

	err := wake(testContext(t), c, apiKey, testOptions(), &bytes.Buffer{})

	assert.True(t, apierrors.IsNotFound(err))
}

// Wake takes the workload's name as status shows it, as well as its
// ManagedWorkload's.
func TestWake_ByWorkloadName(t *testing.T) {
	handWritten := managedWorkload(v1alpha1.PhaseRunning)
	handWritten.Name = "api-mw"
	postgres := managedWorkload(v1alpha1.PhaseRunning)
	postgres.Name = "db"
	postgres.Spec.Target = v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres"}

	tests := []struct {
		name, arg, want string
	}{
		{name: "by the workload", arg: "api", want: "api-mw"},
		{name: "by kind and workload", arg: "statefulset/postgres", want: "db"},
		{name: "by the ManagedWorkload", arg: "db", want: "db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, interceptor.Funcs{}, handWritten.DeepCopy(), postgres.DeepCopy())
			opts := testOptions()
			opts.wait = false

			require.NoError(t, wake(testContext(t), c, client.ObjectKey{Namespace: "preview-42", Name: tt.arg}, opts,
				&bytes.Buffer{}))

			var w v1alpha1.ManagedWorkload
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "preview-42", Name: tt.want}, &w))
			assert.Equal(t, "2026-10-03T09:00:00Z", w.Annotations[v1alpha1.AnnotationWakeRequested])
		})
	}
}

func TestWake_AmbiguousName(t *testing.T) {
	deployment := managedWorkload(v1alpha1.PhasePaused)
	deployment.Name = "api-deploy"
	statefulSet := managedWorkload(v1alpha1.PhasePaused)
	statefulSet.Name = "api-sts"
	statefulSet.Spec.Target.Kind = v1alpha1.TargetKindStatefulSet
	c := newClient(t, interceptor.Funcs{}, deployment, statefulSet)

	err := wake(testContext(t), c, client.ObjectKey{Namespace: "preview-42", Name: "api"}, testOptions(), &bytes.Buffer{})

	require.ErrorIs(t, err, errAmbiguous)
	assert.EqualError(t, err, "api in preview-42 matches more than one ManagedWorkload: "+
		"api-deploy (deployment/api), api-sts (statefulset/api); name one by its workload's kind and name, "+
		"such as deployment/api")
}

// A ManagedWorkload named api that manages another workload doesn't win
// over one that manages a workload named api: the bare name means either,
// and the workload picks one.
func TestWake_NameOfOneTargetOfAnother(t *testing.T) {
	backend := managedWorkload(v1alpha1.PhasePaused)
	backend.Spec.Target.Name = "backend"
	frontend := managedWorkload(v1alpha1.PhasePaused)
	frontend.Name = "frontend-mw"
	opts := testOptions()
	opts.wait = false

	err := wake(testContext(t), newClient(t, interceptor.Funcs{}, backend.DeepCopy(), frontend.DeepCopy()), apiKey,
		opts, &bytes.Buffer{})

	require.ErrorIs(t, err, errAmbiguous)
	assert.ErrorContains(t, err, "api (deployment/backend), frontend-mw (deployment/api)")

	for arg, want := range map[string]string{"deployment/backend": "api", "deployment/api": "frontend-mw"} {
		t.Run(arg, func(t *testing.T) {
			c := newClient(t, interceptor.Funcs{}, backend.DeepCopy(), frontend.DeepCopy())

			require.NoError(t, wake(testContext(t), c, client.ObjectKey{Namespace: "preview-42", Name: arg}, opts,
				&bytes.Buffer{}))

			var w v1alpha1.ManagedWorkload
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "preview-42", Name: want}, &w))
			assert.NotEmpty(t, w.Annotations[v1alpha1.AnnotationWakeRequested])
		})
	}
}

// When a Deployment and a StatefulSet share a name, the operator names one
// ManagedWorkload after the workload and the other after its kind too. The
// bare name then means either, and the kind picks one.
func TestWake_SharedWorkloadName(t *testing.T) {
	deployment := managedWorkload(v1alpha1.PhasePaused)
	statefulSet := managedWorkload(v1alpha1.PhasePaused)
	statefulSet.Name = "api-statefulset"
	statefulSet.Spec.Target.Kind = v1alpha1.TargetKindStatefulSet
	opts := testOptions()
	opts.wait = false

	err := wake(testContext(t), newClient(t, interceptor.Funcs{}, deployment.DeepCopy(), statefulSet.DeepCopy()),
		apiKey, opts, &bytes.Buffer{})

	require.ErrorIs(t, err, errAmbiguous)

	for arg, want := range map[string]string{"statefulset/api": "api-statefulset", "deployment/api": "api"} {
		t.Run(arg, func(t *testing.T) {
			c := newClient(t, interceptor.Funcs{}, deployment.DeepCopy(), statefulSet.DeepCopy())

			require.NoError(t, wake(testContext(t), c, client.ObjectKey{Namespace: "preview-42", Name: arg}, opts,
				&bytes.Buffer{}))

			var w v1alpha1.ManagedWorkload
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "preview-42", Name: want}, &w))
			assert.NotEmpty(t, w.Annotations[v1alpha1.AnnotationWakeRequested])
		})
	}
}

// Whatever phase the workload is in, wake says what happens, and waits for
// Hybernate's answer and then until it's Running.
func TestWake_FromEachPhase(t *testing.T) {
	tests := []struct {
		phase   v1alpha1.WorkloadPhase
		dryRun  bool
		reason  string
		wantOut string
	}{
		{phase: v1alpha1.PhasePausing, reason: "Woken",
			wantOut: "preview-42/api is pausing; it wakes once the pause finishes...\npreview-42/api is Running after 0s\n"},
		{phase: v1alpha1.PhaseResuming, reason: "AlreadyAwake",
			wantOut: "preview-42/api is already waking...\npreview-42/api is Running after 0s\n"},
		{phase: v1alpha1.PhaseIdle, reason: "AlreadyAwake",
			wantOut: "preview-42/api was idle; marked active, it stays up...\npreview-42/api is Running after 0s\n"},
		{phase: v1alpha1.PhaseIdle, dryRun: true, reason: "AlreadyAwake",
			wantOut: "preview-42/api is in dry-run, so Hybernate never paused it; its idle clock restarts now\n"},
		{phase: v1alpha1.PhasePaused, dryRun: true, reason: "Woken",
			wantOut: "waking preview-42/api...\npreview-42/api is Running after 0s\n"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s dry-run=%t", tt.phase, tt.dryRun), func(t *testing.T) {
			w := managedWorkload(tt.phase)
			w.Spec.DryRun = tt.dryRun
			c := newClient(t, wakes(tt.reason, 3), w)
			var out bytes.Buffer

			require.NoError(t, wake(testContext(t), c, apiKey, testOptions(), &out))

			assert.Equal(t, tt.wantOut, out.String())
			assert.Equal(t, "2026-10-03T09:00:00Z", annotations(t, c)[v1alpha1.AnnotationWakeRequested])
		})
	}
}

func TestWake_NotManaged(t *testing.T) {
	ignored := managedWorkload(v1alpha1.PhaseRunning)
	ignored.Status.Conditions = []metav1.Condition{{Type: "TargetAvailable", Status: metav1.ConditionFalse,
		Reason: "TargetIgnored", Message: "Deployment api has hybernate.io/ignore label"}}
	c := newClient(t, interceptor.Funcs{}, ignored)

	err := wake(testContext(t), c, apiKey, testOptions(), &bytes.Buffer{})

	require.ErrorIs(t, err, errNotWakeable)
	assert.Contains(t, err.Error(), "Deployment api has hybernate.io/ignore label")
}

func TestWake_NotInstalled(t *testing.T) {
	funcs := interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object,
		...client.GetOption) error {
		return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "hybernate.io", Kind: "ManagedWorkload"}}
	}}

	err := wake(testContext(t), newClient(t, funcs), apiKey, testOptions(), &bytes.Buffer{})

	assert.ErrorIs(t, err, errNotInstalled)
}

func TestWake_Forbidden(t *testing.T) {
	funcs := interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object,
		...client.GetOption) error {
		return apierrors.NewForbidden(schema.GroupResource{Group: "hybernate.io", Resource: "managedworkloads"}, "api",
			errors.New("user can't get"))
	}}

	err := wake(testContext(t), newClient(t, funcs), apiKey, testOptions(), &bytes.Buffer{})

	require.True(t, apierrors.IsForbidden(err))
	assert.Contains(t, err.Error(),
		`getting ManagedWorkload preview-42/api: managedworkloads.hybernate.io "api" is forbidden`)
}
