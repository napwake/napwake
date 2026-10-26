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

package gitops

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Undo says whether a GitOps tool will set a paused workload's replicas
// back from Git, undoing the pause.
type Undo int

const (
	// Leaves means Git doesn't set the replicas, or the tool is told to
	// leave them.
	Leaves Undo = iota
	// Undoes means the tool sets them from Git and nothing tells it to
	// leave them.
	Undoes
	// MayUndo means the tool sets them from Git, and whether it's told to
	// leave them couldn't be read.
	MayUndo
)

// Replicas is who sets a workload's replicas from Git, and whether that
// undoes a pause.
type Replicas struct {
	// Writer last set the replicas; a GitOps tool when Writer.FromGit.
	Writer Writer
	Undo   Undo
	// App is the Argo CD Application that applies the workload, as
	// namespace/name, when it was found.
	App string
	// Fix is what to change so the tool leaves the replicas, one line per
	// step: only the steps found missing, when that could be read.
	Fix []string
	// Unread says what couldn't be read, for MayUndo.
	Unread string
}

// readTimeout bounds each read of an Argo CD Application or its
// ConfigMap.
const readTimeout = 10 * time.Second

// argoNamespace is where Argo CD is installed by default, with its
// Applications and its argocd-cm ConfigMap.
const argoNamespace = "argocd"

// argoConfigMap is Argo CD's main ConfigMap, where ignoreDifferences can be
// set for every Application.
const argoConfigMap = "argocd-cm"

// respectIgnoreDifferences is the sync option that has a sync leave the
// fields an Application ignores, and not only its diff.
const respectIgnoreDifferences = "RespectIgnoreDifferences=true"

var applicationList = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"}

// Checker tells whether the GitOps tools that set workloads' replicas leave
// them to Napwake once it pauses them. Managed fields say who sets the
// replicas from Git. For Argo CD, which owns them even once it's told to
// ignore them, its Application and argocd-cm say whether it is. Each is read
// once, however many workloads are checked, with the reader's own access:
// one it may not read leaves the answer MayUndo. It's safe for concurrent
// use.
type Checker struct {
	mu      sync.Mutex
	r       client.Reader
	listed  bool
	apps    []unstructured.Unstructured
	listErr error
	byKey   map[client.ObjectKey]applicationRead
	configs map[string]configRead
}

type applicationRead struct {
	app *unstructured.Unstructured
	err error
}

type configRead struct {
	cm  *corev1.ConfigMap
	err error
}

// NewChecker reads with r, with whatever access it has.
func NewChecker(r client.Reader) *Checker {
	return &Checker{r: r, byKey: map[client.ObjectKey]applicationRead{}, configs: map[string]configRead{}}
}

// Replicas says who sets the workload's replicas, and whether they'll undo
// a pause.
func (c *Checker) Replicas(ctx context.Context, workload client.Object) Replicas {
	c.mu.Lock()
	defer c.mu.Unlock()
	writer, found := ReplicasWriter(workload.GetManagedFields())
	if !found || !writer.FromGit() {
		return Replicas{Writer: writer, Undo: Leaves}
	}
	switch writer.Tool {
	case ArgoCD:
		return c.argoCD(ctx, workload, writer)
	case Kapp:
		// kapp writes the replicas it keeps with a rebase rule as its own,
		// and the rule lives in the kapp Config it was run with.
		return Replicas{Writer: writer, Undo: MayUndo, Fix: Fix(Kapp),
			Unread: "kapp's rebase rules aren't recorded on the workload"}
	}
	// Flux, Fleet and Kluctl only set the fields their manifests have, so
	// owning the replicas means Git sets them.
	return Replicas{Writer: writer, Undo: Undoes, Fix: Fix(writer.Tool)}
}

func (c *Checker) argoCD(ctx context.Context, workload client.Object, writer Writer) Replicas {
	out := Replicas{Writer: writer, Undo: MayUndo, Fix: Fix(ArgoCD)}
	key, ok := argoApplicationOf(workload)
	if !ok {
		out.Unread = "it has no Argo CD tracking annotation or label naming its Application"
		return out
	}
	app, err := c.application(ctx, key)
	if err != nil {
		out.Unread = err.Error()
		return out
	}
	out.App = app.GetNamespace() + "/" + app.GetName()

	kind := kindOf(workload)
	respects := syncsRespectIgnores(app)
	ignored := appIgnoresReplicas(app, kind, workload)
	var configErr error
	if !ignored {
		ignored, configErr = c.configIgnoresReplicas(ctx, app.GetNamespace(), kind)
	}
	switch {
	case respects && ignored:
		return Replicas{Writer: writer, Undo: Leaves, App: out.App}
	case respects:
		out.Fix = argoIgnoreFix
	case ignored:
		out.Fix = []string{fmt.Sprintf("Add %s to the syncPolicy.syncOptions of Argo CD application %s, so syncs "+
			"leave the replicas it ignores.", respectIgnoreDifferences, out.App)}
	}
	if configErr == nil || !respects {
		out.Undo = Undoes
		return out
	}
	out.Unread = configErr.Error()
	return out
}

// argoApplicationOf is the Application that applies obj, by the tracking
// annotation Argo CD adds, its default since 3.0, then the label it tracks
// by when set to: its own instance label, or app.kubernetes.io/instance,
// the default before 3.0. An Application in a
// namespace other than Argo CD's own is named namespace_name. The namespace
// is empty when the name doesn't say.
func argoApplicationOf(obj client.Object) (client.ObjectKey, bool) {
	name := ""
	if id := obj.GetAnnotations()["argocd.argoproj.io/tracking-id"]; id != "" {
		name, _, _ = strings.Cut(id, ":")
	} else {
		labels := obj.GetLabels()
		name = labels["argocd.argoproj.io/instance"]
		if name == "" {
			name = labels["app.kubernetes.io/instance"]
		}
	}
	if name == "" {
		return client.ObjectKey{}, false
	}
	if ns, app, ok := strings.Cut(name, "_"); ok {
		return client.ObjectKey{Namespace: ns, Name: app}, true
	}
	return client.ObjectKey{Name: name}, true
}

// application reads the Application named by key: from its namespace when
// that's known; otherwise from every namespace, or, without access to them
// all, from Argo CD's default one.
func (c *Checker) application(ctx context.Context, key client.ObjectKey) (*unstructured.Unstructured, error) {
	if key.Namespace != "" {
		return c.getApplication(ctx, key)
	}
	apps, err := c.listApplications(ctx)
	if apierrors.IsForbidden(err) {
		return c.getApplication(ctx, client.ObjectKey{Namespace: argoNamespace, Name: key.Name})
	}
	if err != nil {
		return nil, err
	}
	var found []*unstructured.Unstructured
	for i := range apps {
		if apps[i].GetName() == key.Name {
			found = append(found, &apps[i])
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no Argo CD Application named %s is in this cluster", key.Name)
	case 1:
		return found[0], nil
	}
	if i := slices.IndexFunc(found, func(a *unstructured.Unstructured) bool {
		return a.GetNamespace() == argoNamespace
	}); i >= 0 {
		return found[i], nil
	}
	return nil, fmt.Errorf("more than one namespace has an Argo CD Application named %s", key.Name)
}

func (c *Checker) listApplications(ctx context.Context) ([]unstructured.Unstructured, error) {
	if !c.listed {
		ctx, cancel := context.WithTimeout(ctx, readTimeout)
		defer cancel()
		var list unstructured.UnstructuredList
		list.SetGroupVersionKind(applicationList)
		c.listed, c.listErr = true, c.r.List(ctx, &list)
		c.apps = list.Items
		c.listErr = applicationError(c.listErr, "")
	}
	return c.apps, c.listErr
}

func (c *Checker) getApplication(ctx context.Context, key client.ObjectKey) (*unstructured.Unstructured, error) {
	if read, ok := c.byKey[key]; ok {
		return read.app, read.err
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(applicationList.GroupVersion().WithKind("Application"))
	err := applicationError(c.r.Get(ctx, key, app), key.String())
	if err != nil {
		app = nil
	}
	c.byKey[key] = applicationRead{app: app, err: err}
	return app, err
}

// applicationError says why an Application couldn't be read, in words for
// the person running the command. Forbidden is kept, so a list can fall
// back to a get.
func applicationError(err error, name string) error {
	switch {
	case err == nil:
		return nil
	case meta.IsNoMatchError(err):
		return errors.New("this cluster has no Argo CD Applications, so Argo CD may run in another")
	case apierrors.IsForbidden(err):
		return &readError{reason: "your access doesn't allow reading Argo CD's Applications", err: err}
	case apierrors.IsNotFound(err):
		return fmt.Errorf("no Argo CD Application %s is in this cluster", name)
	}
	return fmt.Errorf("reading Argo CD's Applications: %w", err)
}

// readError words an API error for a person, and keeps it for errors.Is
// and the apierrors checks.
type readError struct {
	reason string
	err    error
}

func (e *readError) Error() string { return e.reason }
func (e *readError) Unwrap() error { return e.err }

func syncsRespectIgnores(app *unstructured.Unstructured) bool {
	options, _, _ := unstructured.NestedStringSlice(app.Object, "spec", "syncPolicy", "syncOptions")
	return slices.ContainsFunc(options, func(o string) bool { return strings.TrimSpace(o) == respectIgnoreDifferences })
}

// ignoreRule is one ignoreDifferences entry, in an Application or in
// argocd-cm.
type ignoreRule struct {
	Group             string   `json:"group"`
	Kind              string   `json:"kind"`
	Name              string   `json:"name"`
	Namespace         string   `json:"namespace"`
	JSONPointers      []string `json:"jsonPointers"`
	JQPathExpressions []string `json:"jqPathExpressions"`
}

// ignoresReplicas says the rule leaves out spec.replicas, or all of a
// workload's spec.
func (r ignoreRule) ignoresReplicas() bool {
	for _, p := range r.JSONPointers {
		if p == "/spec/replicas" || p == "/spec" {
			return true
		}
	}
	for _, q := range r.JQPathExpressions {
		if q = strings.Join(strings.Fields(q), ""); q == ".spec.replicas" || q == ".spec" {
			return true
		}
	}
	return false
}

// appIgnoresReplicas says the Application's own ignoreDifferences leave out
// the workload's replicas. Its group and kind may be globs, as in Argo CD.
func appIgnoresReplicas(app *unstructured.Unstructured, kind string, workload client.Object) bool {
	entries, _, _ := unstructured.NestedSlice(app.Object, "spec", "ignoreDifferences")
	for _, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		var rule ignoreRule
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(fields, &rule); err != nil {
			continue
		}
		if globMatch(rule.Group, appsv1.GroupName) && globMatch(rule.Kind, kind) &&
			(rule.Name == "" || rule.Name == workload.GetName()) &&
			(rule.Namespace == "" || rule.Namespace == workload.GetNamespace()) && rule.ignoresReplicas() {
			return true
		}
	}
	return false
}

func globMatch(pattern, value string) bool {
	ok, err := path.Match(pattern, value)
	return err == nil && ok
}

// configIgnoresReplicas says argocd-cm leaves out the replicas of every
// workload of kind, in a resource.customizations.ignoreDifferences key for
// the kind or for all, or in the older resource.customizations block. It's
// read from the Application's namespace, or Argo CD's default one, for an
// Application in another.
func (c *Checker) configIgnoresReplicas(ctx context.Context, namespace, kind string) (bool, error) {
	tried := []string{namespace}
	cm, err := c.config(ctx, namespace)
	if apierrors.IsNotFound(err) && namespace != argoNamespace {
		tried = append(tried, argoNamespace)
		cm, err = c.config(ctx, argoNamespace)
	}
	switch {
	case apierrors.IsForbidden(err):
		return false, fmt.Errorf("your access doesn't allow reading Argo CD's %s ConfigMap", argoConfigMap)
	case apierrors.IsNotFound(err):
		return false, fmt.Errorf("no %s ConfigMap is in %s", argoConfigMap, strings.Join(tried, " or "))
	case err != nil:
		return false, fmt.Errorf("reading Argo CD's %s ConfigMap: %w", argoConfigMap, err)
	}
	for _, key := range []string{"apps_" + kind, "all"} {
		var rule ignoreRule
		if value, ok := cm.Data["resource.customizations.ignoreDifferences."+key]; ok &&
			yaml.Unmarshal([]byte(value), &rule) == nil && rule.ignoresReplicas() {
			return true, nil
		}
	}
	var legacy map[string]struct {
		IgnoreDifferences string `json:"ignoreDifferences"`
	}
	if yaml.Unmarshal([]byte(cm.Data["resource.customizations"]), &legacy) == nil {
		var rule ignoreRule
		if value, ok := legacy[appsv1.GroupName+"/"+kind]; ok &&
			yaml.Unmarshal([]byte(value.IgnoreDifferences), &rule) == nil && rule.ignoresReplicas() {
			return true, nil
		}
	}
	return false, nil
}

func (c *Checker) config(ctx context.Context, namespace string) (*corev1.ConfigMap, error) {
	if read, ok := c.configs[namespace]; ok {
		return read.cm, read.err
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	cm := &corev1.ConfigMap{}
	err := c.r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: argoConfigMap}, cm)
	if err != nil {
		cm = nil
	}
	c.configs[namespace] = configRead{cm: cm, err: err}
	return cm, err
}

func kindOf(obj client.Object) string {
	switch obj.(type) {
	case *appsv1.Deployment:
		return "Deployment"
	case *appsv1.StatefulSet:
		return "StatefulSet"
	}
	return obj.GetObjectKind().GroupVersionKind().Kind
}
