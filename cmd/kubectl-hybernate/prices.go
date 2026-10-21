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
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/okedeji/hybernate/internal/discovery"
)

// prices are the rates for what node prices can't price, and whether each
// is the built-in assumption rather than the user's own.
type prices struct {
	CPUPerHour    float64 `json:"cpuPerHour"`
	MemoryPerHour float64 `json:"memoryPerHour"`
	CPUAssumed    bool    `json:"cpuAssumed"`
	MemoryAssumed bool    `json:"memoryAssumed"`
}

// ownPrices says which prices the user gave.
func ownPrices(cmd *cobra.Command) (cpu, memory bool) {
	return cmd.Flags().Changed("cpu-price"), cmd.Flags().Changed("memory-price")
}

func pricesFor(opts scanOptions) prices {
	return prices{
		CPUPerHour:    opts.cpuPrice,
		MemoryPerHour: opts.memoryPrice,
		CPUAssumed:    !opts.ownCPUPrice,
		MemoryAssumed: !opts.ownMemoryPrice,
	}
}

func (p prices) cpu() string    { return fmt.Sprintf("$%.3f per vCPU-hour", p.CPUPerHour) }
func (p prices) memory() string { return fmt.Sprintf("$%.3f per GiB-hour of memory", p.MemoryPerHour) }

// sentence says what costs were priced at without node prices, and which
// prices are the built-in assumption rather than the user's own.
func (p prices) sentence() string {
	switch {
	case p.CPUAssumed && p.MemoryAssumed:
		return "Assumed list prices: " + p.cpu() + " and " + p.memory() + ", from AWS on-demand in us-east-1."
	case p.CPUAssumed:
		return "Your memory price, " + p.memory() + ", and the assumed CPU price, " + p.cpu() +
			" (AWS on-demand, us-east-1)."
	case p.MemoryAssumed:
		return "Your CPU price, " + p.cpu() + ", and the assumed memory price, " + p.memory() +
			" (AWS on-demand, us-east-1)."
	default:
		return "Your prices: " + p.cpu() + " and " + p.memory() + "."
	}
}

// pricesSentence says what the scan's costs were priced at: the list
// prices of the cluster's node types where it has them, on-demand or spot,
// the user's own prices, or assumed ones; which workloads run on spot nodes
// priced at spot; and which cost less than shown, as they're on spot nodes
// of a type without a spot price, priced on-demand.
func (r scanResult) pricesSentence() string {
	p := r.Prices
	if !p.CPUAssumed && !p.MemoryAssumed {
		return p.sentence()
	}
	var nodes discovery.Prices
	var atSpot, spotAtOnDemand int
	if r.ClusterReport != nil {
		nodes = r.NodePrices
		for _, wl := range r.Workloads {
			if wl.OnSpot {
				atSpot++
			}
			if wl.SpotAtOnDemand {
				spotAtOnDemand++
			}
		}
	}

	var listed, unlisted []discovery.NodeTypePrice
	unlistedNodes := 0
	for _, t := range nodes.NodeTypes {
		if t.Listed {
			listed = append(listed, t)
			continue
		}
		unlisted = append(unlisted, t)
		unlistedNodes += t.Nodes
	}

	var s string
	switch {
	case nodes.NodesUnread != "":
		s = p.sentence() + " The nodes weren't read: " + nodes.NodesUnread + "."
	case len(listed) == 0:
		s = p.sentence()
		if unlistedNodes > 0 {
			s += " None of the nodes has a list price: " + unlistedPhrase(unlisted) + "."
		}
	default:
		s = "The " + nodeTypesPhrase(listed) + "."
		if !p.CPUAssumed {
			s += " CPU at your price, " + p.cpu() + "."
		}
		if !p.MemoryAssumed {
			s += " Memory at your price, " + p.memory() + "."
		}
		if unlistedNodes > 0 {
			use := "use"
			if unlistedNodes == 1 {
				use = "uses"
			}
			s += " " + unlistedPhrase(unlisted) + " " + use + " " + p.unlistedRates() + "."
		}
	}
	if atSpot > 0 {
		s += fmt.Sprintf(" %s on spot nodes, priced at spot.", plural(atSpot, "workload runs", "workloads run"))
	}
	if spotAtOnDemand > 0 {
		costs := "they cost"
		if spotAtOnDemand == 1 {
			costs = "it costs"
		}
		s += fmt.Sprintf(" %s on spot nodes of a type without a spot price%s, priced at on-demand, so %s less "+
			"than shown.", plural(spotAtOnDemand, "workload runs", "workloads run"), spotUnpriced(nodes.NodeTypes),
			costs)
	}
	return s
}

// spotUnpriced names, in brackets, the spot nodes' types that have no spot
// price.
func spotUnpriced(types []discovery.NodeTypePrice) string {
	var names []string
	for _, t := range types {
		if t.Spot && !t.AtSpot && t.InstanceType != "" && !slices.Contains(names, t.InstanceType) {
			names = append(names, t.InstanceType)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return " (" + joinAnd(names) + ")"
}

// unlistedRates names the rates pods on nodes without a list price use.
func (p prices) unlistedRates() string {
	switch {
	case p.CPUAssumed && p.MemoryAssumed:
		return "the assumed " + p.cpu() + " and " + p.memory() + " (AWS on-demand, us-east-1)"
	case p.CPUAssumed:
		return "the assumed " + p.cpu() + " (AWS on-demand, us-east-1) and your memory price"
	default:
		return "your CPU price and the assumed " + p.memory() + " (AWS on-demand, us-east-1)"
	}
}

// costsSentence says what the scan's costs were priced at, as the
// terminal and the report both put it.
func (r scanResult) costsSentence() string {
	return "Costs use " + lowerFirst(r.pricesSentence())
}

// nodeTypesPhrase names the list prices the cluster's node types were
// priced at, on-demand, spot or both, and where they are.
func nodeTypesPhrase(types []discovery.NodeTypePrice) string {
	var names, places []string
	onDemand, spot := false, false
	for _, t := range types {
		if !slices.Contains(names, t.InstanceType) {
			names = append(names, t.InstanceType)
		}
		place := providerName(t.Provider) + " " + t.Region
		if !slices.Contains(places, place) {
			places = append(places, place)
		}
		spot = spot || t.AtSpot
		onDemand = onDemand || !t.AtSpot
	}
	slices.Sort(places)
	kinds := "on-demand"
	switch {
	case spot && onDemand:
		kinds = "on-demand and spot"
	case spot:
		kinds = "spot"
	}
	where := ", " + joinAnd(places)
	if len(names) == 1 {
		price := "list price"
		if spot && onDemand {
			price = "list prices"
		}
		return fmt.Sprintf("%s %s of the cluster's nodes (%s%s)", kinds, price, names[0], where)
	}
	const shown = 3
	listed := joinAnd(names)
	if len(names) > shown {
		listed = fmt.Sprintf("%s and %d more", strings.Join(names[:shown], ", "), len(names)-shown)
	}
	return fmt.Sprintf("%s list prices of the cluster's %d node types (%s%s)", kinds, len(names), listed, where)
}

// unlistedPhrase names the nodes without a list price, by why: no instance
// type label, no region label to price their type in, or a type the price
// table doesn't have.
func unlistedPhrase(types []discovery.NodeTypePrice) string {
	const (
		noType = iota
		noRegion
		notInTable
	)
	phrases := []string{noType: "without an instance type", noRegion: "without a region label",
		notInTable: "of a type without a list price"}
	nodes := make([]int, len(phrases))
	names := make([][]string, len(phrases))
	for _, t := range types {
		why := notInTable
		switch {
		case t.InstanceType == "":
			why = noType
		case t.Region == "":
			why = noRegion
		}
		nodes[why] += t.Nodes
		if t.InstanceType != "" && !slices.Contains(names[why], t.InstanceType) {
			names[why] = append(names[why], t.InstanceType)
		}
	}
	var parts []string
	for why, phrase := range phrases {
		if nodes[why] == 0 {
			continue
		}
		part := plural(nodes[why], "node", "nodes") + " " + phrase
		if len(names[why]) > 0 {
			part += " (" + strings.Join(names[why], ", ") + ")"
		}
		parts = append(parts, part)
	}
	return joinAnd(parts)
}

func providerName(provider string) string {
	switch provider {
	case "aws":
		return "AWS"
	case "gcp":
		return "Google Cloud"
	case "azure":
		return "Azure"
	}
	return provider
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
