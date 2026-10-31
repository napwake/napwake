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
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/napwake/napwake/internal/discovery"
)

// A share link is the address of napwake.dev's page for one share card's
// percentage, carrying the card's own numbers and nothing else (cmo-075):
// never the cluster's name, the Prometheus source, or any text. The page
// on napwake.dev draws the card again from them, so a share leads back to
// the scan.
const (
	shareSite    = "https://napwake.dev/s/"
	shareVersion = "1"
	// The percentages napwake.dev has a page for, as couldFor and the
	// status page's month allow them.
	shareLeast, shareMost = 1, 100
)

// cardAmount is an amount as dollars or money writes it on a card, such as
// $1,234, $446 or $7.59. <$1, <$0.01 and a negative amount aren't
// numbers a link can carry.
var cardAmount = regexp.MustCompile(`^\$(\d{1,3}(?:,\d{3})*)(\.\d{2})?$`)

// plainAmount is an amount a card writes as the plain number a share link
// carries, $1,234 as 1234 and $7.59 as 7.59, and false for one that isn't a
// number.
func plainAmount(card string) (string, bool) {
	m := cardAmount.FindStringSubmatch(card)
	if m == nil {
		return "", false
	}
	return strings.ReplaceAll(m[1], ",", "") + m[2], true
}

// shareAmounts is the plain numbers of a card's amounts, what the
// workloads cost with Napwake and without it, then any others, or false
// when one isn't a number, or without is none, which no percentage is of.
func shareAmounts(with, without string, others ...string) ([]string, bool) {
	var plain []string
	for _, card := range append([]string{with, without}, others...) {
		n, ok := plainAmount(card)
		if !ok {
			return nil, false
		}
		plain = append(plain, n)
	}
	if v, err := strconv.ParseFloat(plain[1], 64); err != nil || v == 0 {
		return nil, false
	}
	return plain, true
}

// scanShareLink is the share link for the scan page's "less" card, the
// month's before and after it leads with where the bill can drop, from the
// numbers the card shows: its percentage, what the workloads could cost a
// month and what they cost, and how many days of Prometheus history they're
// from, to three places, or 0 for a snapshot. It's "" where the page shows
// no such card, as for an estimate from too little history, or where an
// amount on it isn't a number, such as <$1. The page and the terminal both
// take it from here, so they always agree.
func scanShareLink(page reportPage, history *discovery.HistorySource) string {
	c := page.Could
	if len(page.Workloads) == 0 || c == nil || page.NodeScale.State == scaleNone ||
		page.Headline.ShortHistory != "" || c.Percent < shareLeast || c.Percent > shareMost {
		return ""
	}
	amounts, ok := shareAmounts(c.With, c.Without)
	if !ok {
		return ""
	}
	days := 0.0
	if page.History && history != nil && history.Hours > 0 {
		days = math.Round(history.Hours/24*1000) / 1000
	}
	return fmt.Sprintf("%s%d/?v=%s&with=%s&without=%s&src=%s", shareSite, c.Percent, shareVersion, amounts[0],
		amounts[1], strconv.FormatFloat(days, 'f', -1, 64))
}

// statusShareLink is the share link for the status page's "less" card,
// what comes off the bill at this month's pace, from the numbers the card
// shows: its percentage, the bill and what it would be without Napwake,
// what nodes scaling down take off it, and the days counted. It's "" where
// the page shows no such card, or where an amount on it isn't a number,
// such as <$0.01.
func statusShareLink(page statusPage) string {
	m := page.Month
	if len(page.Workloads) == 0 || m == nil || (m.State != monthSteady && m.State != monthEarly) || !m.OnTheBill ||
		m.SavedPercent < shareLeast || m.SavedPercent > shareMost {
		return ""
	}
	amounts, ok := shareAmounts(m.Bill, m.Without, m.Saved)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%sless-%d/?v=%s&bill=%s&without=%s&saved=%s&days=%d", shareSite, m.SavedPercent,
		shareVersion, amounts[0], amounts[1], amounts[2], m.Days)
}
