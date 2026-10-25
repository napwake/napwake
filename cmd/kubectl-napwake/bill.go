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

package main

import (
	"strings"

	"github.com/napwake/napwake/internal/nodescale"
)

// What decides whether freed capacity comes off the bill.
const (
	// scaleFound is a node autoscaler, which removes the nodes pausing
	// empties.
	scaleFound = "found"
	// scalePods is a cluster billed by what its pods request, as GKE
	// Autopilot is, where the bill drops as pods stop.
	scalePods = "pods"
	scaleNone = "none"
	// scaleUnknown is a user whose access can't read what scales nodes.
	scaleUnknown = "unknown"
)

// nodeScale is what turns freed capacity into money, for the pages' Your
// bill row and the terminal's line: the autoscalers found, with how they
// remove nodes, none, with the guide to setting one up, or unknown, with
// what couldn't be read.
type nodeScale struct {
	State  string
	Kinds  []string
	Detail string
	FixURL string
}

func nodeScaleFor(a nodescale.Autoscaler) nodeScale {
	switch {
	case !a.Known():
		return nodeScale{State: scaleUnknown, Detail: joinOr(a.Unread)}
	case len(a.Scalers) == 0:
		return nodeScale{State: scaleNone, FixURL: nodeScalingGuide}
	case a.Has(nodescale.GKEAutopilot):
		return nodeScale{State: scalePods, Kinds: []string{string(nodescale.GKEAutopilot)}}
	}
	ns := nodeScale{State: scaleFound}
	var details []string
	for _, s := range a.Scalers {
		name := string(s.Kind)
		if s.Kind == nodescale.ClusterAutoscaler && s.Provider != "" {
			name += " on " + s.Provider
		}
		ns.Kinds = append(ns.Kinds, name)
		subject, rest := scalerDetail(s, a.NodePools)
		switch {
		case rest == "":
		case subject == "":
			details = append(details, rest)
		case len(a.Scalers) == 1:
			details = append(details, "it "+rest)
		default:
			details = append(details, subject+" "+rest)
		}
	}
	ns.Detail = strings.Join(details, "; ")
	return ns
}

// scalerDetail says how an autoscaler removes nodes, beyond its name: how
// Karpenter's NodePools consolidate, said of Karpenter when they all do it
// alike, or that a Cluster Autoscaler may not be running, when only a hint
// shows it. The subject is what a line naming one autoscaler calls "it",
// and empty when the rest names its own.
func scalerDetail(s nodescale.Scaler, pools []nodescale.NodePool) (subject, rest string) {
	switch s.Kind {
	case nodescale.Karpenter, nodescale.EKSAutoMode:
		c := consolidation(pools)
		if strings.HasPrefix(c, "NodePool ") {
			return "", c
		}
		return string(s.Kind), c
	case nodescale.ClusterAutoscaler:
		if s.Hint {
			return string(s.Kind), "may not be running, as all that shows it is " + s.Evidence
		}
	}
	return "", ""
}

// billLine is the terminal's Your bill line, in the pages' words.
func billLine(a nodescale.Autoscaler) string {
	ns := nodeScaleFor(a)
	switch ns.State {
	case scaleUnknown:
		return "Your bill: your access can't read the node autoscaler (" + ns.Detail + ")"
	case scaleNone:
		return "Your bill: won't drop until something removes nodes, and no node autoscaler was found. See " +
			ns.FixURL
	case scalePods:
		return "Your bill: drops as pods stop, since " + joinOr(ns.Kinds) + " bills what pods request"
	}
	line := "Your bill: drops only as " + strings.Join(ns.Kinds, " or ") + " removes nodes"
	if ns.Detail != "" {
		line += " (" + ns.Detail + ")"
	}
	return line
}
