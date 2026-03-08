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
package provider

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/library"
)

type LifeCycleManager interface {
	Name() string
	Apply(ctx context.Context, subscription *library.Subscription) error
}

// Retain Life Cycle Manager

type RetainLifeCycleManager struct {
	TTL time.Duration
}

func (manager *RetainLifeCycleManager) Name() string {
	return "Retain Life Cylce Manager"
}

func (manager *RetainLifeCycleManager) Apply(ctx context.Context, subscription *library.Subscription) error {
	return nil
}

// TTL Life Cycle Manager

type TTLLifeCycleManager struct {
	TTL time.Duration
}

func (manager *TTLLifeCycleManager) Name() string {
	return "TTL Life Cylce Manager"
}

func (manager *TTLLifeCycleManager) Apply(ctx context.Context, subscription *library.Subscription) error {
	return nil
}
