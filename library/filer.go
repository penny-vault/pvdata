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
	"strings"

	"github.com/penny-vault/pvdata/backblaze"
	"github.com/penny-vault/pvdata/data"
)

// FilerFromSpec resolves a per-subscription filer spec to a
// data.Filer. Supported schemes:
//
//	file:///abs/path          → write to local filesystem
//	b2://<bucket>/<prefix>    → upload to Backblaze B2 (public)
//
// Returns nil for an unrecognised scheme; callers treat that as
// "no filer configured".
func FilerFromSpec(spec string) data.Filer {
	switch {
	case strings.HasPrefix(spec, "file://"):
		return data.NewFilerFromString(spec)
	case strings.HasPrefix(spec, "b2://"):
		rest := strings.TrimPrefix(spec, "b2://")
		parts := strings.SplitN(rest, "/", 2)

		bucket := parts[0]
		prefix := ""

		if len(parts) == 2 {
			prefix = parts[1]
		}

		return backblaze.NewFiler(bucket, prefix)
	}

	return nil
}
