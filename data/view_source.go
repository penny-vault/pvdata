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
package data

import "time"

// ViewSource represents a single table contributing to a published view,
// optionally bounded by a date range. FromDate is inclusive, UntilDate is
// exclusive. The bounds are silently ignored for data types whose DateColumn
// is empty (e.g. asset descriptions).
type ViewSource struct {
	TableName      string     `json:"table_name"`
	SubscriptionID string     `json:"subscription_id"`
	FromDate       *time.Time `json:"from_date,omitempty"`
	UntilDate      *time.Time `json:"until_date,omitempty"`
}
