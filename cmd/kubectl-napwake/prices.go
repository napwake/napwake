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

	"github.com/napwake/napwake/internal/discovery"
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
	n := r.nodePricing()

	var s string
	switch {
	case n.NodesUnread != "":
		s = p.sentence() + " The nodes weren't read: " + n.NodesUnread + "."
	case len(n.listed) == 0:
		s = p.sentence()
		if n.unlistedNodes > 0 {
			s += " None of the nodes has a list price: " + unlistedPhrase(n.unlisted) + "."
		}
	default:
		s = "The " + nodeTypesPhrase(n.listed) + "."
		if !p.CPUAssumed {
			s += " CPU at your price, " + p.cpu() + "."
		}
		if !p.MemoryAssumed {
			s += " Memory at your price, " + p.memory() + "."
		}
		if n.unlistedNodes > 0 {
			use := "use"
			if n.unlistedNodes == 1 {
				use = "uses"
			}
			s += " " + unlistedPhrase(n.unlisted) + " " + use + " " + p.unlistedRates() + "."
		}
	}
	if n.atSpot > 0 {
		s += fmt.Sprintf(" %s on spot nodes, priced at spot.", plural(n.atSpot, "workload runs", "workloads run"))
	}
	if n.spotAtOnDemand > 0 {
		costs := "they cost"
		if n.spotAtOnDemand == 1 {
			costs = "it costs"
		}
		s += fmt.Sprintf(" %s on spot nodes of a type without a spot price%s, priced at on-demand, so %s less "+
			"than shown.", plural(n.spotAtOnDemand, "workload runs", "workloads run"), spotUnpriced(n.NodeTypes),
			costs)
	}
	return s
}

// nodePricing is how the scan's nodes were priced: their types, with and
// without a list price, and how many workloads run on spot nodes, priced
// at spot or, without a spot price for their type, at on-demand.
type nodePricing struct {
	discovery.Prices
	listed, unlisted       []discovery.NodeTypePrice
	unlistedNodes          int
	atSpot, spotAtOnDemand int
}

func (r scanResult) nodePricing() nodePricing {
	var n nodePricing
	if r.ClusterReport == nil {
		return n
	}
	n.Prices = r.NodePrices
	for _, wl := range r.Workloads {
		if wl.OnSpot {
			n.atSpot++
		}
		if wl.SpotAtOnDemand {
			n.spotAtOnDemand++
		}
	}
	for _, t := range n.NodeTypes {
		if t.Listed {
			n.listed = append(n.listed, t)
			continue
		}
		n.unlisted = append(n.unlisted, t)
		n.unlistedNodes += t.Nodes
	}
	return n
}

// What a pricing's prices are.
const (
	pricedAtList  = "list"
	pricedYours   = "yours"
	pricedAssumed = "assumed"
)

// pricing is what the scan's costs were priced at, for the short Prices
// row and line: the list prices of the cluster's node types, all in one
// place, the user's own prices, or the assumed ones where no node has a
// list price. NodeTypes names up to three, then how many more. A cluster
// the short form can't say truly, such as one with some nodes priced and
// others not, has none, and the full sentence says it instead.
type pricing struct {
	Source                string
	Provider, Region      string
	NodeTypes             []string
	Spot                  int
	CPUPrice, MemoryPrice string
}

func (r scanResult) pricing() *pricing {
	p := r.Prices
	rates := func(source string) *pricing {
		return &pricing{Source: source, CPUPrice: fmt.Sprintf("$%.3f", p.CPUPerHour),
			MemoryPrice: fmt.Sprintf("$%.3f", p.MemoryPerHour)}
	}
	if !p.CPUAssumed && !p.MemoryAssumed {
		return rates(pricedYours)
	}
	n := r.nodePricing()
	if !p.CPUAssumed || !p.MemoryAssumed || n.NodesUnread != "" || n.spotAtOnDemand > 0 {
		return nil
	}
	if len(n.listed) == 0 {
		if n.unlistedNodes == 0 {
			return nil
		}
		assumed := rates(pricedAssumed)
		assumed.Provider, assumed.Region = "AWS", "us-east-1"
		return assumed
	}
	if n.unlistedNodes > 0 {
		return nil
	}
	short := &pricing{Source: pricedAtList, Provider: providerName(n.listed[0].Provider), Region: n.listed[0].Region,
		Spot: n.atSpot}
	for _, t := range n.listed {
		if t.Provider != n.listed[0].Provider || t.Region != n.listed[0].Region {
			return nil
		}
		if !slices.Contains(short.NodeTypes, t.InstanceType) {
			short.NodeTypes = append(short.NodeTypes, t.InstanceType)
		}
	}
	const shown = 3
	if more := len(short.NodeTypes) - shown; more > 0 {
		short.NodeTypes = append(short.NodeTypes[:shown:shown], fmt.Sprintf("%d more", more))
	}
	return short
}

// pricesLine is the scan's Prices line, short where it can be, and the
// full sentence where it can't.
func (r scanResult) pricesLine() string {
	p := r.pricing()
	if p == nil {
		return r.costsSentence()
	}
	switch p.Source {
	case pricedYours:
		return fmt.Sprintf("Prices: yours, %s per vCPU-hour and %s per GiB-hour", p.CPUPrice, p.MemoryPrice)
	case pricedAssumed:
		return fmt.Sprintf("Prices: assumed, %s %s on-demand at %s per vCPU-hour and %s per GiB-hour, as your nodes "+
			"have no list price", p.Provider, p.Region, p.CPUPrice, p.MemoryPrice)
	}
	line := "Prices: " + p.Provider + " " + p.Region + " list prices · "
	if len(p.NodeTypes) == 1 {
		line += p.NodeTypes[0]
	} else {
		line += countOf(r.nodePricing().distinctListedTypes(), "node type")
	}
	if p.Spot > 0 {
		line += " · " + countOf(p.Spot, "workload") + " on spot, priced at spot"
	}
	return line
}

// distinctListedTypes is how many instance types have a list price.
func (n nodePricing) distinctListedTypes() int {
	var names []string
	for _, t := range n.listed {
		if !slices.Contains(names, t.InstanceType) {
			names = append(names, t.InstanceType)
		}
	}
	return len(names)
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

// costsSentence says what the scan's costs were priced at in full, for the
// clusters pricesLine can't say in short.
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
