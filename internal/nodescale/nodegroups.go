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

package nodescale

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// NodeGroup is one of Cluster Autoscaler's node groups, as its status
// ConfigMap reports it: its name, the sizes it's kept within, and the size
// it's at.
type NodeGroup struct {
	Name             string
	MinSize, MaxSize int
	Target           int
}

// atMinimum reports whether Cluster Autoscaler can't remove a node of the
// group: it's at the least it may have.
func (g NodeGroup) atMinimum() bool {
	return g.Target <= g.MinSize
}

// unreadStatus is how Unread names the status ConfigMap.
const unreadStatus = "Cluster Autoscaler's status ConfigMap in kube-system"

// readNodeGroups reads Cluster Autoscaler's node groups from its status
// ConfigMap. Without the ConfigMap there are none; without access to it,
// ok is false.
func readNodeGroups(ctx context.Context, r client.Reader) (groups []NodeGroup, ok bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var cm corev1.ConfigMap
	err = r.Get(ctx, client.ObjectKey{Namespace: metav1.NamespaceSystem, Name: clusterAutoscalerStatus}, &cm)
	switch {
	case apierrors.IsNotFound(err):
		return nil, true, nil
	case apierrors.IsForbidden(err):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("getting ConfigMap kube-system/%s: %w", clusterAutoscalerStatus, err)
	}
	return parseNodeGroups(cm.Data["status"]), true, nil
}

// statusYAML is the part of the status Cluster Autoscaler writes as YAML
// since 1.30 that says each node group's sizes.
type statusYAML struct {
	NodeGroups []struct {
		Name   string `json:"name"`
		Health struct {
			CloudProviderTarget int `json:"cloudProviderTarget"`
			MinSize             int `json:"minSize"`
			MaxSize             int `json:"maxSize"`
		} `json:"health"`
	} `json:"nodeGroups"`
}

// textStatusPrefix starts the status Cluster Autoscaler wrote as text
// before 1.30.
const textStatusPrefix = "Cluster-autoscaler status at "

var (
	textGroupName = regexp.MustCompile(`^\s*Name:\s+(\S+)`)
	// textGroupSizes is the node group's Health line, which ends
	// "cloudProviderTarget=2 (minSize=2, maxSize=4))".
	textGroupSizes = regexp.MustCompile(`^\s*Health:.*cloudProviderTarget=(\d+) \(minSize=(\d+), maxSize=(\d+)\)`)
)

// parseNodeGroups reads the node groups from the status Cluster Autoscaler
// writes: YAML since 1.30, or text before. One it can't read has none.
func parseNodeGroups(status string) []NodeGroup {
	if strings.HasPrefix(strings.TrimSpace(status), textStatusPrefix) {
		return parseTextNodeGroups(status)
	}
	var s statusYAML
	if err := yaml.Unmarshal([]byte(status), &s); err != nil {
		return nil
	}
	var groups []NodeGroup
	for _, g := range s.NodeGroups {
		groups = append(groups, NodeGroup{Name: g.Name, MinSize: g.Health.MinSize, MaxSize: g.Health.MaxSize,
			Target: g.Health.CloudProviderTarget})
	}
	return groups
}

func parseTextNodeGroups(status string) []NodeGroup {
	var groups []NodeGroup
	var name string
	scanner := bufio.NewScanner(strings.NewReader(status))
	for scanner.Scan() {
		line := scanner.Text()
		if m := textGroupName.FindStringSubmatch(line); m != nil {
			name = m[1]
			continue
		}
		m := textGroupSizes.FindStringSubmatch(line)
		if m == nil || name == "" {
			continue
		}
		target, _ := strconv.Atoi(m[1]) // the pattern matched digits
		minSize, _ := strconv.Atoi(m[2])
		maxSize, _ := strconv.Atoi(m[3])
		groups = append(groups, NodeGroup{Name: name, MinSize: minSize, MaxSize: maxSize, Target: target})
		name = ""
	}
	return groups
}

// groupOf is the node group a node is in, by how each provider names them:
// a GKE instance group, …/instanceGroups/<prefix>-grp, names its nodes
// <prefix>-<suffix>; an EKS managed node group's Auto Scaling group is
// eks-<node group>-<UUID>, the node group its nodes are labelled with; and
// an AKS scale set, <name>-vmss, names its nodes <name>-vmss and six
// characters more. A node whose group can't be told isn't in one.
func (c *Cluster) groupOf(node *corev1.Node) (NodeGroup, bool) {
	for _, g := range c.NodeGroups {
		if inNodeGroup(node, g.Name) {
			return g, true
		}
	}
	return NodeGroup{}, false
}

var asgUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// aksInstanceID is how many characters an AKS node's name adds to its
// scale set's.
const aksInstanceID = 6

func inNodeGroup(node *corev1.Node, group string) bool {
	base := shortGroupName(group)
	if prefix, ok := strings.CutSuffix(base, "-grp"); ok {
		return strings.HasPrefix(node.Name, prefix+"-")
	}
	if ng := node.Labels[labelEKSNodeGroup]; ng != "" {
		rest, ok := strings.CutPrefix(base, "eks-"+ng+"-")
		return ok && asgUUID.MatchString(rest)
	}
	if strings.HasSuffix(base, "-vmss") {
		return strings.HasPrefix(node.Name, base) && len(node.Name) == len(base)+aksInstanceID
	}
	return false
}

// shortGroupName is a node group's name without the path a GKE instance
// group's URL carries.
func shortGroupName(group string) string {
	return group[strings.LastIndex(group, "/")+1:]
}

// atMinimumReason says a node stays for its node group's minimum.
func atMinimumReason(g NodeGroup) Reason {
	return Reason{Code: CodeAtMinimum, Detail: fmt.Sprintf("its node group, %s, is at its minimum of %s",
		shortGroupName(g.Name), countNodes(g.MinSize))}
}
