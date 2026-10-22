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

package discovery

import (
	"context"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

// Install is how a Hybernate install runs its doorman, as its operator's
// flags say, which decides whether a request wakes a paused workload it
// manages. The zero Install is Hybernate's default: the doorman on, the NEG
// forwarder off.
type Install struct {
	// Namespace is where its operator runs.
	Namespace string
	// Watches are the namespaces it manages, every one when empty.
	Watches []string
	// DoormanOff says requests don't wake its paused workloads: its
	// operator runs with --doorman-service empty, as doorman.enabled: false
	// has the chart run it.
	DoormanOff bool
	// NEGForwarder says Services behind GKE NEGs are routed through its
	// forwarder, with --neg-forwarder.
	NEGForwarder bool
}

// readInstalls finds Hybernate's operators by the labels its chart and
// manifests give their Deployments, across the cluster, and reads their
// flags. When none can be read, as without access to their namespaces, the
// replay assumes Hybernate's defaults.
func (s *Scanner) readInstalls(ctx context.Context) []Install {
	var installs []Install
	err := listAll(ctx, s.client, func(list *appsv1.DeploymentList) {
		for i := range list.Items {
			if d := &list.Items[i]; isHybernateApp(d.Labels) {
				installs = append(installs, installOf(d))
			}
		}
	}, client.MatchingLabels{"control-plane": "controller-manager"})
	if err != nil {
		return nil
	}
	return installs
}

func isHybernateApp(labels map[string]string) bool {
	return labels["app.kubernetes.io/name"] == hybernateApp || chartName(labels["helm.sh/chart"]) == hybernateApp
}

// installOf is the install whose operator d runs, from its manager
// container's flags, or its first container's.
func installOf(d *appsv1.Deployment) Install {
	containers := d.Spec.Template.Spec.Containers
	in := Install{Namespace: d.Namespace}
	if len(containers) == 0 {
		return in
	}
	c := containers[0]
	if i := slices.IndexFunc(containers, func(c corev1.Container) bool { return c.Name == "manager" }); i >= 0 {
		c = containers[i]
	}
	args := append(slices.Clone(c.Command), c.Args...)
	if service, ok := flagValue(args, "doorman-service"); ok && service == "" {
		in.DoormanOff = true
	}
	in.NEGForwarder = boolFlag(args, "neg-forwarder")
	if watched, ok := flagValue(args, "watch-namespaces"); ok {
		for ns := range strings.SplitSeq(watched, ",") {
			if ns = strings.TrimSpace(ns); ns != "" {
				in.Watches = append(in.Watches, ns)
			}
		}
	}
	return in
}

// flagValue is the value args give a string flag, as Go's flag package
// reads them, the last one given winning, and whether they give one.
func flagValue(args []string, name string) (string, bool) {
	var value string
	found := false
	for i := 0; i < len(args); i++ {
		n, v, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !strings.HasPrefix(args[i], "-") || n != name {
			continue
		}
		found, value = true, v
		if !hasValue && i+1 < len(args) {
			i++
			value = args[i]
		}
	}
	return value, found
}

// boolFlag is whether args set a bool flag, which takes no separate value.
func boolFlag(args []string, name string) bool {
	set := false
	for _, a := range args {
		n, v, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || n != name {
			continue
		}
		set = true
		if hasValue {
			set, _ = strconv.ParseBool(v)
		}
	}
	return set
}

// installFor is the install that would manage a workload in namespace: the
// one its ManagedWorkload mw names, then one that watches namespace by
// name, then one that watches every namespace. None is Hybernate's default.
func installFor(installs []Install, namespace string, mw *v1alpha1.ManagedWorkload) Install {
	if mw != nil {
		if owner := mw.Annotations[v1alpha1.AnnotationOperator]; owner != "" {
			if i := slices.IndexFunc(installs, func(in Install) bool { return in.Namespace == owner }); i >= 0 {
				return installs[i]
			}
		}
	}
	if i := slices.IndexFunc(installs, func(in Install) bool { return slices.Contains(in.Watches, namespace) }); i >= 0 {
		return installs[i]
	}
	if i := slices.IndexFunc(installs, func(in Install) bool { return len(in.Watches) == 0 }); i >= 0 {
		return installs[i]
	}
	return Install{}
}
