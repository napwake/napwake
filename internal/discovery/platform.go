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
	"cmp"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

// The Platform of the infrastructure Hybernate itself protects.
const (
	PlatformKubernetes = "Kubernetes"
	PlatformHybernate  = "Hybernate"
)

// hybernateApp is the name Hybernate's manifests and chart label its
// workloads with, and the chart's own.
const hybernateApp = "hybernate"

// kubernetesNamespaces are Kubernetes' own namespaces, and the platform
// namespaces distributions add, which Hybernate never pauses.
var kubernetesNamespaces = []string{"kube-*", "openshift-*", "cattle-*", "gke-*", "gmp-*"}

// platformNamespaces are where well-known cluster infrastructure installs
// by default: ingress, certificates, GitOps, service meshes, policy,
// monitoring, storage and the like, which run the cluster rather than an
// app, and are never idle in the sense pausing means.
var platformNamespaces = []string{
	"hybernate-system",
	"monitoring", "prometheus", "observability", "kube-prometheus-stack", "grafana", "loki", "logging", "tempo",
	"jaeger", "datadog", "newrelic", "dynatrace", "elastic-system", "amazon-cloudwatch", "aws-observability",
	"ingress-nginx", "nginx-ingress", "traefik", "kong", "contour", "projectcontour", "envoy-gateway-system",
	"haproxy-controller",
	"cert-manager", "external-dns", "external-secrets", "sealed-secrets", "vault",
	"argocd", "argo-cd", "argo-rollouts", "flux-system",
	"istio-system", "istio-ingress", "linkerd", "linkerd-viz", "linkerd-jaeger", "cilium", "calico-system",
	"calico-apiserver", "tigera-operator", "metallb-system",
	"gatekeeper-system", "kyverno", "falco", "trivy-system",
	"keda", "karpenter", "velero", "longhorn-system", "rook-ceph", "openebs", "local-path-storage",
	"kubernetes-dashboard", "crossplane-system", "knative-serving", "knative-eventing",
}

// platformApps are well-known infrastructure by the name its workloads are
// labelled with, in app.kubernetes.io/name, app.kubernetes.io/part-of, app
// or k8s-app, or the Helm chart that installed it, wherever it runs.
var platformApps = []string{
	hybernateApp,
	"cert-manager", "external-dns", "external-secrets", "sealed-secrets", "vault",
	"ingress-nginx", "traefik", "kong", "contour", "aws-load-balancer-controller",
	"argocd", "argo-cd", "argo-rollouts", "flux",
	"istio", "istiod", "linkerd", "cilium", "calico", "coredns", "kube-dns",
	"gatekeeper", "kyverno",
	"kube-prometheus-stack", "prometheus", "prometheus-operator", "alertmanager", "grafana", "loki", "promtail",
	"tempo", "fluent-bit", "fluentd", "kube-state-metrics", "prometheus-node-exporter", "metrics-server", "datadog",
	"keda", "karpenter", "cluster-autoscaler", "velero",
}

// appLabels are the labels a workload names the software it runs with.
var appLabels = []string{"app.kubernetes.io/name", "app.kubernetes.io/part-of", "app", "k8s-app"}

// namespacePlatform says what infrastructure a namespace holds by its name,
// or "" for one an app could be in.
func namespacePlatform(namespace string) string {
	for _, pattern := range kubernetesNamespaces {
		if ok, _ := path.Match(pattern, namespace); ok {
			return PlatformKubernetes
		}
	}
	if namespace == "hybernate-system" {
		return PlatformHybernate
	}
	if slices.Contains(platformNamespaces, namespace) {
		return namespace
	}
	return ""
}

// appPlatform says what well-known infrastructure a workload runs, by the
// labels on it and its pods, or "" for an app.
func appPlatform(wl workload) string {
	for _, set := range []map[string]string{wl.obj.GetLabels(), wl.template.Labels} {
		for _, label := range appLabels {
			if v := set[label]; slices.Contains(platformApps, v) {
				return platformName(v)
			}
		}
		if chart := chartName(set["helm.sh/chart"]); slices.Contains(platformApps, chart) {
			return platformName(chart)
		}
	}
	return ""
}

func platformName(app string) string {
	if app == hybernateApp {
		return PlatformHybernate
	}
	return app
}

// chartName is the chart in a helm.sh/chart label, which Helm charts write
// as the chart's name, a dash, and its version.
func chartName(label string) string {
	i := strings.LastIndex(label, "-")
	if i <= 0 || i == len(label)-1 {
		return ""
	}
	version := strings.TrimPrefix(label[i+1:], "v")
	if version == "" || version[0] < '0' || version[0] > '9' {
		return ""
	}
	return label[:i]
}

// platformOf says what part of the cluster's own infrastructure each of a
// namespace's workloads is, by kind and name. A namespace Hybernate runs in
// is all Hybernate's, as the operator protects it. A namespace labelled
// allow-protected is scanned like any other, as Hybernate manages it then,
// and so is a workload someone already has Hybernate managing. The
// operator's and the doorman's own Deployments are Hybernate's however their
// namespace is labelled, as the operator never pauses its own components.
//
// Other infrastructure, found by its namespace's name or its labels, is left
// out only as the scan's guess at what nobody means to pause: the operator
// pauses it like any app once it's opted in. So it isn't guessed at in a
// namespace labelled managed, which says its workloads are meant to pause,
// such as a preview environment with its own Vault or Grafana.
func platformOf(namespace string, namespaceLabels map[string]string, workloads []workload,
	managed map[string]*v1alpha1.ManagedWorkload) map[string]string {
	allowed := namespaceLabels[v1alpha1.LabelAllowProtected] == v1alpha1.True
	optedIn := namespaceLabels[v1alpha1.LabelManaged] == v1alpha1.True
	var whole string
	if !allowed {
		whole = namespacePlatform(namespace)
	}
	apps := make(map[string]string, len(workloads))
	for _, wl := range workloads {
		app := appPlatform(wl)
		if app == PlatformHybernate && !allowed {
			whole = PlatformHybernate
		}
		apps[string(wl.kind)+"/"+wl.obj.GetName()] = app
	}
	out := make(map[string]string, len(workloads))
	for _, wl := range workloads {
		key := string(wl.kind) + "/" + wl.obj.GetName()
		app := apps[key]
		switch {
		case hybernateComponent(wl):
			out[key] = PlatformHybernate
		case allowed:
		case whole == PlatformKubernetes || whole == PlatformHybernate:
			out[key] = whole
		case managed[key] != nil, optedIn:
		case whole != "":
			out[key] = whole
		case app != "":
			out[key] = app
		}
	}
	return out
}

// hybernateComponent says the workload is the operator or the doorman, by
// the labels Hybernate's manifests and chart give their Deployments.
func hybernateComponent(wl workload) bool {
	labels := wl.obj.GetLabels()
	switch labels["control-plane"] {
	case "controller-manager", "doorman":
		return labels["app.kubernetes.io/name"] == hybernateApp || chartName(labels["helm.sh/chart"]) == hybernateApp
	}
	return false
}

// splitPlatform separates the apps from the cluster's own infrastructure,
// and drops apps' dependencies on Kubernetes' and Hybernate's, which
// Hybernate never pauses, so needn't wake. A dependency on other
// infrastructure stays, as the operator pauses that once it's opted in.
func splitPlatform(workloads []Workload) (apps, platform []Workload) {
	neverPaused := map[workloadKey]bool{}
	for _, w := range workloads {
		if w.Platform == "" {
			continue
		}
		platform = append(platform, w)
		if w.Platform == PlatformKubernetes || w.Platform == PlatformHybernate {
			neverPaused[workloadKey{w.Namespace, w.Kind, w.Name}] = true
		}
	}
	apps = make([]Workload, 0, len(workloads)-len(platform))
	for _, w := range workloads {
		if w.Platform != "" {
			continue
		}
		w.Dependencies = slices.DeleteFunc(w.Dependencies, func(d Dependency) bool {
			return neverPaused[workloadKey{d.Namespace, d.Kind, d.Name}]
		})
		apps = append(apps, w)
	}
	return apps, platform
}

// platformNote says which workloads were left out as the cluster's own
// infrastructure, by namespace, most first, and then, apart, which of it
// Hybernate would pause: what's left out of a scan's totals isn't what the
// operator protects.
func platformNote(platform []Workload) string {
	byNamespace := map[string]int{}
	for _, w := range platform {
		byNamespace[w.Namespace]++
	}
	namespaces := slices.SortedFunc(maps.Keys(byNamespace), func(a, b string) int {
		return cmp.Or(cmp.Compare(byNamespace[b], byNamespace[a]), strings.Compare(a, b))
	})
	listed := make([]string, 0, len(namespaces))
	for _, ns := range namespaces {
		listed = append(listed, fmt.Sprintf("%s (%d)", ns, byNamespace[ns]))
	}
	return fmt.Sprintf("%s left out of the totals and next steps: %s. Hybernate never pauses Kubernetes' own "+
		"workloads or itself; anything else, infrastructure included, pauses only if you opt it in",
		plural(len(platform), "infrastructure workload is", "infrastructure workloads are"), listSome(listed))
}

func oneOrMany(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
