// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package library

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xeonx/timeago"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Summary returns a description of the library in markdown
func (myLibrary *Library) Summary(ctx context.Context) (string, error) {
	p := message.NewPrinter(language.English)
	builder := strings.Builder{}

	if _, err := fmt.Fprintf(&builder, "# %s\n", myLibrary.Name); err != nil {
		return "", err
	}

	if _, err := builder.WriteString("## Details\n\n"); err != nil {
		return "", err
	}

	// Database connection string
	if _, err := fmt.Fprintf(&builder, "Database: %s\n\n", myLibrary.DBUrl); err != nil {
		return "", err
	}

	// Number of subscriptions
	numSubscriptions, err := myLibrary.NumSubscriptions(ctx)
	if err != nil {
		return "", err
	}

	if _, err := builder.WriteString(p.Sprintf("  * Num Subscriptions: %d\n", numSubscriptions)); err != nil {
		return "", err
	}

	// Total securities count
	totalSecurities, err := myLibrary.TotalSecurities(ctx)
	if err != nil {
		return "", err
	}

	if _, err := builder.WriteString(p.Sprintf("  * Securities Tracked: %d\n", totalSecurities)); err != nil {
		return "", err
	}

	// Total record count
	totalRecords, err := myLibrary.TotalRecords(ctx)
	if err != nil {
		return "", err
	}

	if _, err := builder.WriteString(p.Sprintf("  * Total Records: %d\n\n", totalRecords)); err != nil {
		return "", err
	}

	// Last updated time
	lastUpdated, err := myLibrary.LastUpdated(ctx)
	if err != nil {
		return "", err
	}

	age := timeago.English.Format(lastUpdated)

	if lastUpdated.Equal(time.Time{}) {
		if _, err := builder.WriteString("Last Updated: Never\n\n"); err != nil {
			return "", err
		}
	} else {
		if _, err := fmt.Fprintf(&builder, "Last Updated: %s (%s)\n\n", age, lastUpdated.Local().Format("01/02/2006")); err != nil {
			return "", err
		}
	}

	// Subscriptions
	if _, err := builder.WriteString("## Subscriptions\n\n"); err != nil {
		return "", err
	}

	subscriptions, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		return "", err
	}

	for _, subscription := range subscriptions {
		if !subscription.Active {
			continue
		}

		lastDate := "present"
		if time.Until(subscription.LastObsDate) < (-30 * 24 * time.Hour) {
			lastDate = subscription.LastObsDate.Format("Jan 2006")
		}

		if _, err := builder.WriteString(p.Sprintf("  * %s %s (%s - %s) [%s]\n", subscription.Provider,
			subscription.Dataset, subscription.FirstObsDate.Format("Jan 2006"), lastDate, subscription.ID.String()[:6])); err != nil {
			return "", err
		}

		for _, dataType := range subscription.DataTypes {
			if _, err := builder.WriteString(p.Sprintf("    * %s\n", dataType)); err != nil {
				return "", err
			}
		}
	}

	if _, err := builder.WriteString("## Inactive subscriptions\n\n"); err != nil {
		return "", err
	}

	for _, subscription := range subscriptions {
		if subscription.Active {
			continue
		}

		if _, err := builder.WriteString(p.Sprintf("  * %s %s [%s]\n", subscription.Provider,
			subscription.Dataset, subscription.ID.String()[:6])); err != nil {
			return "", err
		}
	}

	return builder.String(), nil
}
