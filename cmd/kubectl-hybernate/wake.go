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
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

// errNotWakeable means Hybernate isn't managing the workload, so a wake
// request would only be refused.
var errNotWakeable = errors.New("can't be woken")

// errNotWoken means Hybernate answered the wake request without waking the
// workload, and said why.
var errNotWoken = errors.New("wasn't woken")

// errNotRunningYet means the wake was asked for, but the workload wasn't
// Running when the wait ran out.
var errNotRunningYet = errors.New("isn't Running yet")

// What the operator's WakeRequest condition says came of a request.
const (
	conditionWakeRequest = "WakeRequest"
	reasonWoken          = "Woken"
)

type wakeOptions struct {
	keepAwake    time.Duration
	wait         bool
	timeout      time.Duration
	pollInterval time.Duration
	now          func() time.Time
}

func wakeCmd(kube *kubeFlags) *cobra.Command {
	opts := wakeOptions{timeout: 5 * time.Minute, pollInterval: 2 * time.Second, now: time.Now}
	var namespace string

	cmd := &cobra.Command{
		Use:   "wake NAME",
		Short: "Wake a paused workload and wait until it's Running",
		Long: `Wake asks Hybernate to wake a workload now, whatever its wake setting: a
paused one wakes, with the workloads it depends on, and a running one's idle
clock restarts. By default it waits for Hybernate's answer, and then until
the workload is Running.

Underneath, it sets the hybernate.io/wake-requested annotation on the
workload's ManagedWorkload to a fresh token, as any tool can, and the
ManagedWorkload's WakeRequest condition says what came of it.

NAME is the workload, as status shows it, such as api or statefulset/postgres,
or its ManagedWorkload's name.

Examples:
  # Wake a workload and wait for it
  kubectl hybernate wake api -n preview-42

  # Keep it awake for the next two hours, for a demo
  kubectl hybernate wake api -n preview-42 --for 2h

  # Return as soon as the wake is requested
  kubectl hybernate wake api -n preview-42 --wait=false`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkPositive("timeout", opts.timeout); err != nil {
				return err
			}
			if opts.keepAwake < 0 {
				return errors.New("--for can't be negative")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			defer cancel()
			k8s, at, err := kube.client(ctx)
			if err != nil {
				return fmt.Errorf("building kubernetes client: %w", err)
			}
			if namespace == "" {
				namespace = at.namespace
			}
			err = wake(ctx, k8s, client.ObjectKey{Namespace: namespace, Name: args[0]}, opts, cmd.OutOrStdout())
			if errors.Is(err, errNotRunningYet) || errors.Is(err, errNotWoken) {
				return err
			}
			return timedOut(ctx, err, opts.timeout)
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "",
		"Namespace of the workload (defaults to the kubeconfig context's)")
	cmd.Flags().DurationVar(&opts.keepAwake, "for", 0,
		"Also keep the workload awake for this long, e.g. 2h, unless it's held awake longer already")
	cmd.Flags().BoolVar(&opts.wait, "wait", true, "Wait for Hybernate's answer, and then until the workload is Running")
	addTimeoutFlag(cmd, &opts.timeout, "How long to wait")
	return cmd
}

// wake asks Hybernate to wake the workload with a fresh
// hybernate.io/wake-requested token, and, if asked, waits for its answer and
// then until the workload is Running. key names the workload or its
// ManagedWorkload.
func wake(ctx context.Context, c client.Client, key client.ObjectKey, opts wakeOptions, out io.Writer) error {
	w, err := findManaged(ctx, c, key.Namespace, key.Name)
	if err != nil {
		return err
	}
	if err := checkWakeable(w); err != nil {
		return err
	}
	key = client.ObjectKeyFromObject(w)
	ref := key.Namespace + "/" + w.Spec.Target.Name

	start := opts.now()
	token := start.UTC().Format(time.RFC3339Nano)
	before := w.Status.LastWakeRequest
	patch := client.MergeFrom(w.DeepCopy())
	if w.Annotations == nil {
		w.Annotations = map[string]string{}
	}
	w.Annotations[v1alpha1.AnnotationWakeRequested] = token
	held := activeUntil(w.Annotations)
	keepUntil := start.Add(opts.keepAwake)
	if opts.keepAwake > 0 && !held.After(keepUntil) {
		w.Annotations[v1alpha1.AnnotationActiveUntil] = keepUntil.UTC().Format(time.RFC3339)
	}
	if err := c.Patch(ctx, w, patch); err != nil {
		return fmt.Errorf("asking Hybernate to wake %s: %w", ref, err)
	}
	if opts.keepAwake > 0 && held.After(keepUntil) {
		_, _ = fmt.Fprintf(out, "%s is already held awake until %s, later than --for asks, so that stays\n", ref,
			held.UTC().Format(time.RFC3339))
	}

	awake := awakeMessage(ref, w)
	if awake == "" {
		_, _ = fmt.Fprintf(out, "%s\n", wakeMessage(ref, w.Status.Phase, opts.wait))
	} else if !opts.wait {
		_, _ = fmt.Fprintf(out, "%s\n", awake)
	}
	if !opts.wait {
		return nil
	}
	return waitForWake(ctx, c, key, ref, wakeRequest{token: token, before: before, awake: awake}, w.Status.Phase,
		start, opts, out)
}

// awakeMessage says what a wake request does for a workload that's awake
// and not about to pause, which it finds awake and restarts the idle clock
// of, or is empty for one it wakes. A ManagedWorkload Hybernate hasn't
// taken on yet, with no phase, is taken on Running.
func awakeMessage(ref string, w *v1alpha1.ManagedWorkload) string {
	switch phase := w.Status.Phase; {
	case w.Spec.DryRun && phase != v1alpha1.PhasePaused && phase != v1alpha1.PhasePausing:
		return ref + " is in dry-run, so Hybernate never paused it; its idle clock restarts now"
	case phase == v1alpha1.PhaseRunning, phase == "", phase == v1alpha1.PhaseCreating:
		return ref + " is already running; its idle clock restarts now"
	}
	return ""
}

// wakeRequest is a request wake made, to wait for the answer to.
type wakeRequest struct {
	token string
	// before is the last request Hybernate had answered when wake asked.
	before string
	// awake is what to say once a workload that was awake is answered as
	// such, or empty for one that wakes.
	awake string
}

// answers reports whether lastWakeRequest, the last request Hybernate
// answered, answers req: its own token, or one another tool set after it,
// which replaced it on the annotation, so what came of that one came of
// req too. A status lost meanwhile answers nothing.
func (req wakeRequest) answers(lastWakeRequest string) bool {
	return lastWakeRequest == req.token || lastWakeRequest != "" && lastWakeRequest != req.before
}

// waitForWake waits until Hybernate has answered req, and then until the
// workload is Running, or Hybernate has said why it didn't wake it. phase
// is the workload's when the wait starts.
func waitForWake(ctx context.Context, c client.Client, key client.ObjectKey, ref string, req wakeRequest,
	phase v1alpha1.WorkloadPhase, start time.Time, opts wakeOptions, out io.Writer) error {
	ticker := time.NewTicker(opts.pollInterval)
	defer ticker.Stop()
	answered := false
	notYet := func() error {
		unanswered := ""
		if !answered {
			unanswered = ", and Hybernate hasn't taken up the wake request"
		}
		return fmt.Errorf("%s %w: it's still %s%s (%w); kubectl describe managedworkload %s -n %s shows why",
			ref, errNotRunningYet, phase, unanswered, ctx.Err(), key.Name, key.Namespace)
	}
	var w v1alpha1.ManagedWorkload
	for {
		if err := c.Get(ctx, key, &w); err != nil {
			// The wait ran out during the read, not because the API
			// server stopped answering.
			if ctx.Err() != nil {
				return notYet()
			}
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("%s %w: its ManagedWorkload %s was deleted, and Hybernate handed the workload back "+
					"as it let go", ref, errNotWoken, key.Name)
			}
			return fmt.Errorf("waiting for %s: %w", ref, err)
		}
		phase = w.Status.Phase
		if req.answers(w.Status.LastWakeRequest) {
			answered = true
			answer := meta.FindStatusCondition(w.Status.Conditions, conditionWakeRequest)
			if answer != nil && answer.Status == metav1.ConditionFalse {
				return fmt.Errorf("%s %w: %s", ref, errNotWoken, refusal("not woken", oneLine(answer.Message)))
			}
			// A workload read as awake can have been woken all the same,
			// after a pause already under way when wake read it.
			woken := answer != nil && answer.Reason == reasonWoken
			switch {
			case req.awake != "" && !woken:
				_, _ = fmt.Fprintf(out, "%s\n", req.awake)
				return nil
			case phase == v1alpha1.PhaseRunning:
				_, _ = fmt.Fprintf(out, "%s is Running after %s\n", ref, opts.now().Sub(start).Round(time.Second))
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return notYet()
		case <-ticker.C:
		}
	}
}

// wakeMessage says what the wake does from the phase the workload is in.
func wakeMessage(ref string, phase v1alpha1.WorkloadPhase, wait bool) string {
	var msg string
	switch phase {
	case v1alpha1.PhaseIdle:
		msg = ref + " was idle; marked active, it stays up"
	case v1alpha1.PhasePausing:
		msg = ref + " is pausing; it wakes once the pause finishes"
	case v1alpha1.PhaseResuming:
		msg = ref + " is already waking"
	default:
		msg = "waking " + ref
	}
	if wait {
		msg += "..."
	}
	return msg
}

// checkWakeable rejects the workloads Hybernate isn't managing, so the user
// gets the reason straight away, rather than a request it would refuse.
func checkWakeable(w *v1alpha1.ManagedWorkload) error {
	ref := w.Namespace + "/" + w.Spec.Target.Name
	conditions := w.Status.Conditions
	if c := meta.FindStatusCondition(conditions, "TargetAvailable"); c != nil && c.Status == metav1.ConditionFalse {
		return fmt.Errorf("%s %w: Hybernate isn't managing it: %s", ref, errNotWakeable, oneLine(c.Message))
	}
	if c := meta.FindStatusCondition(conditions, "DuplicateTarget"); c != nil && c.Status == metav1.ConditionTrue {
		return fmt.Errorf("%s %w: %s", ref, errNotWakeable, oneLine(c.Message))
	}
	return nil
}
