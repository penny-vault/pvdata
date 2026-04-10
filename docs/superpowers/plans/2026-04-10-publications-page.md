# Publications Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a web UI for managing published views (CRUD), mirroring the existing TUI flow with list, create, and detail pages.

**Architecture:** New Go handler file `web/handlers_publications.go` with 7 endpoints. Three new Vue pages (list, new, detail) using PrimeVue components. Backend changes to `library/published_views.go` to add a load-by-UUID function and change overlap validation from error to warning info.

**Tech Stack:** Go/Fiber (backend), Vue 3/PrimeVue (frontend), pgx/v5 (database), Ginkgo/Gomega (tests)

---

## File Map

**Create:**
- `web/handlers_publications.go` -- All 7 publication API handlers
- `web/handlers_publications_test.go` -- Ginkgo tests for handler-adjacent logic (enrichment, candidates)
- `web/ui/src/pages/PublicationsPage.vue` -- List page
- `web/ui/src/pages/NewPublicationPage.vue` -- Data type picker for creation
- `web/ui/src/pages/PublicationDetailPage.vue` -- Detail/edit page

**Modify:**
- `library/published_views.go` -- Add `LoadPublishedViewByID`, change `ValidateSources` to return overlap info instead of error
- `library/published_views_test.go` -- Update overlap validation tests
- `web/route.go` -- Register publication routes
- `web/ui/src/lib/api.ts` -- Add 7 publication API functions
- `web/ui/src/router/index.ts` -- Add 3 publication routes
- `web/ui/src/App.vue` -- Add Publications nav item

---

### Task 1: Add `LoadPublishedViewByID` to library

**Files:**
- Modify: `library/published_views.go`
- Modify: `library/published_views_test.go`

- [ ] **Step 1: Write failing test for LoadPublishedViewByID**

In `library/published_views_test.go`, add a new `Describe` block after the existing `ValidateSources` block:

```go
Describe("ValidateSourcesOverlap", func() {
    It("returns overlap info for overlapping date ranges", func() {
        d1 := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
        d2 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
        d3 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
        pv := &library.PublishedView{
            ViewName:    "eod",
            DataTypeKey: "eod",
            Sources: []library.ViewSource{
                {TableName: "t1", FromDate: &d1, UntilDate: &d2},
                {TableName: "t2", FromDate: &d3},
            },
        }
        overlaps := pv.CheckOverlaps()
        Expect(overlaps).To(HaveLen(1))
        Expect(overlaps[0]).To(ContainSubstring("t1"))
        Expect(overlaps[0]).To(ContainSubstring("t2"))
    })

    It("returns empty for non-overlapping sources", func() {
        boundary := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
        pv := &library.PublishedView{
            ViewName:    "eod",
            DataTypeKey: "eod",
            Sources: []library.ViewSource{
                {TableName: "t1", UntilDate: &boundary},
                {TableName: "t2", FromDate: &boundary},
            },
        }
        overlaps := pv.CheckOverlaps()
        Expect(overlaps).To(BeEmpty())
    })

    It("returns empty for single source", func() {
        pv := &library.PublishedView{
            ViewName:    "eod",
            DataTypeKey: "eod",
            Sources: []library.ViewSource{
                {TableName: "t1"},
            },
        }
        overlaps := pv.CheckOverlaps()
        Expect(overlaps).To(BeEmpty())
    })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race --focus "ValidateSourcesOverlap" ./library/`
Expected: FAIL -- `CheckOverlaps` not defined

- [ ] **Step 3: Implement CheckOverlaps and update ValidateSources**

In `library/published_views.go`, add the new `CheckOverlaps` method and update `ValidateSources` to use it. Also add `LoadPublishedViewByID`.

Add `CheckOverlaps` after the existing `ValidateSources` method:

```go
// CheckOverlaps returns human-readable descriptions of any overlapping date
// ranges between sources. Returns an empty slice when there are no overlaps.
func (pv *PublishedView) CheckOverlaps() []string {
	if len(pv.Sources) <= 1 {
		return nil
	}

	type bounded struct {
		from  time.Time
		until time.Time
		name  string
	}

	sentinelMin := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)
	sentinelMax := time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

	items := make([]bounded, len(pv.Sources))
	for i, s := range pv.Sources {
		b := bounded{name: s.TableName, from: sentinelMin, until: sentinelMax}
		if s.FromDate != nil {
			b.from = *s.FromDate
		}

		if s.UntilDate != nil {
			b.until = *s.UntilDate
		}

		items[i] = b
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].from.Before(items[j].from)
	})

	var overlaps []string

	for i := 1; i < len(items); i++ {
		if items[i].from.Before(items[i-1].until) {
			overlaps = append(overlaps, fmt.Sprintf(
				"overlapping date ranges: %s [until %s] and %s [from %s]",
				items[i-1].name, items[i-1].until.Format("2006-01-02"),
				items[i].name, items[i].from.Format("2006-01-02"),
			))
		}
	}

	return overlaps
}
```

Update the existing `ValidateSources` to delegate to `CheckOverlaps`:

```go
func (pv *PublishedView) ValidateSources() error {
	overlaps := pv.CheckOverlaps()
	if len(overlaps) > 0 {
		return fmt.Errorf("%s", overlaps[0])
	}

	return nil
}
```

Add `LoadPublishedViewByID` after the existing `LoadPublishedView` function:

```go
// LoadPublishedViewByID loads a single published view by its UUID.
func LoadPublishedViewByID(ctx context.Context, q Querier, id uuid.UUID) (*PublishedView, error) {
	pv := &PublishedView{}

	var sourcesJSON []byte

	err := q.QueryRow(ctx,
		`SELECT id, view_name, data_type_key, sources FROM published_views WHERE id = $1`,
		id,
	).Scan(&pv.ID, &pv.ViewName, &pv.DataTypeKey, &sourcesJSON)
	if err != nil {
		return nil, fmt.Errorf("load published view %s: %w", id, err)
	}

	if err := json.Unmarshal(sourcesJSON, &pv.Sources); err != nil {
		return nil, fmt.Errorf("unmarshal sources for %s: %w", id, err)
	}

	return pv, nil
}
```

- [ ] **Step 4: Update existing overlap test**

The existing test at line 131-144 of `library/published_views_test.go` ("rejects overlapping date ranges") still expects `ValidateSources` to return an error for overlaps. This behavior is preserved since `ValidateSources` still returns an error via `CheckOverlaps`. No change needed to the existing test.

- [ ] **Step 5: Run all library tests to verify**

Run: `ginkgo run -race ./library/`
Expected: All tests PASS

- [ ] **Step 6: Commit**

```bash
git add library/published_views.go library/published_views_test.go
git commit -m "feat(library): add CheckOverlaps and LoadPublishedViewByID for publications UI"
```

---

### Task 2: Backend publication handlers

**Files:**
- Create: `web/handlers_publications.go`
- Modify: `web/route.go`

- [ ] **Step 1: Create handlers_publications.go with all 7 handlers**

Create `web/handlers_publications.go`:

```go
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
package web

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
)

// EnrichedSource is a ViewSource augmented with subscription metadata for the UI.
type EnrichedSource struct {
	TableName        string  `json:"table_name"`
	SubscriptionID   string  `json:"subscription_id"`
	FromDate         *string `json:"from_date"`
	UntilDate        *string `json:"until_date"`
	SubscriptionName string  `json:"subscription_name"`
	Provider         string  `json:"provider"`
	Dataset          string  `json:"dataset"`
}

// EnrichedPublishedView is a PublishedView with enriched sources and overlap warnings.
type EnrichedPublishedView struct {
	ID          uuid.UUID        `json:"id"`
	ViewName    string           `json:"view_name"`
	DataTypeKey string           `json:"data_type_key"`
	Sources     []EnrichedSource `json:"sources"`
	Overlaps    []string         `json:"overlaps,omitempty"`
}

// PublishedViewSummary is the list response item.
type PublishedViewSummary struct {
	ID          uuid.UUID `json:"id"`
	ViewName    string    `json:"view_name"`
	DataTypeKey string    `json:"data_type_key"`
	SourceCount int       `json:"source_count"`
}

// CandidateSource is a subscription table eligible to be added as a source.
type CandidateSource struct {
	TableName        string `json:"table_name"`
	SubscriptionID   string `json:"subscription_id"`
	SubscriptionName string `json:"subscription_name"`
	Provider         string `json:"provider"`
	Dataset          string `json:"dataset"`
}

// AvailableType is a data type that can have a new published view created.
type AvailableType struct {
	DataTypeKey string `json:"data_type_key"`
	ViewName    string `json:"view_name"`
}

// CreatePublicationRequest is the JSON body for creating a new published view.
type CreatePublicationRequest struct {
	DataTypeKey string `json:"data_type_key"`
}

// UpdatePublicationRequest is the JSON body for updating published view sources.
type UpdatePublicationRequest struct {
	Sources []library.ViewSource `json:"sources"`
}

// GetPublications returns all published views as summaries.
func GetPublications(c *fiber.Ctx) error {
	myLibrary := getLibrary(c)
	ctx := c.UserContext()

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire database connection")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}

	defer conn.Release()

	views, err := library.LoadPublishedViews(ctx, conn)
	if err != nil {
		log.Error().Err(err).Msg("could not load published views")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not load published views",
		})
	}

	summaries := make([]PublishedViewSummary, len(views))
	for i, v := range views {
		summaries[i] = PublishedViewSummary{
			ID:          v.ID,
			ViewName:    v.ViewName,
			DataTypeKey: v.DataTypeKey,
			SourceCount: len(v.Sources),
		}
	}

	return c.JSON(summaries)
}

// CreatePublication creates a new empty published view for a data type.
func CreatePublication(c *fiber.Ctx) error {
	var req CreatePublicationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid request body",
		})
	}

	dt, ok := data.DataTypes[req.DataTypeKey]
	if !ok || dt.ViewName == "" {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid or unsupported data type key",
		})
	}

	myLibrary := getLibrary(c)
	ctx := c.UserContext()

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire database connection")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}

	defer conn.Release()

	pv := &library.PublishedView{
		ViewName:    dt.ViewName,
		DataTypeKey: req.DataTypeKey,
		Sources:     []library.ViewSource{},
	}

	if err := library.SavePublishedView(ctx, conn, pv); err != nil {
		log.Error().Err(err).Msg("could not create published view")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not create published view",
		})
	}

	return c.Status(fiber.StatusCreated).JSON(pv)
}

// GetPublication returns a single published view with enriched source metadata.
func GetPublication(c *fiber.Ctx) error {
	idStr := c.Params("id")

	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid publication ID",
		})
	}

	myLibrary := getLibrary(c)
	ctx := c.UserContext()

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire database connection")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}

	defer conn.Release()

	pv, err := library.LoadPublishedViewByID(ctx, conn, id)
	if err != nil {
		log.Error().Err(err).Str("ID", idStr).Msg("published view not found")

		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "published view not found",
		})
	}

	subs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not load subscriptions for enrichment")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not load subscriptions",
		})
	}

	subMap := make(map[string]*library.Subscription)
	for _, s := range subs {
		subMap[s.ID.String()] = s
	}

	enriched := enrichPublishedView(pv, subMap)

	return c.JSON(enriched)
}

// UpdatePublication updates the sources of a published view.
func UpdatePublication(c *fiber.Ctx) error {
	idStr := c.Params("id")

	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid publication ID",
		})
	}

	var req UpdatePublicationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid request body",
		})
	}

	myLibrary := getLibrary(c)
	ctx := c.UserContext()

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire database connection")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}

	defer conn.Release()

	pv, err := library.LoadPublishedViewByID(ctx, conn, id)
	if err != nil {
		log.Error().Err(err).Str("ID", idStr).Msg("published view not found")

		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "published view not found",
		})
	}

	pv.Sources = req.Sources

	if err := library.SavePublishedView(ctx, conn, pv); err != nil {
		log.Error().Err(err).Msg("could not update published view")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not update published view",
		})
	}

	subs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not load subscriptions for enrichment")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not load subscriptions",
		})
	}

	subMap := make(map[string]*library.Subscription)
	for _, s := range subs {
		subMap[s.ID.String()] = s
	}

	enriched := enrichPublishedView(pv, subMap)

	return c.JSON(enriched)
}

// DeletePublication deletes a published view and its database view.
func DeletePublication(c *fiber.Ctx) error {
	idStr := c.Params("id")

	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid publication ID",
		})
	}

	myLibrary := getLibrary(c)
	ctx := c.UserContext()

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire database connection")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}

	defer conn.Release()

	pv, err := library.LoadPublishedViewByID(ctx, conn, id)
	if err != nil {
		log.Error().Err(err).Str("ID", idStr).Msg("published view not found")

		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "published view not found",
		})
	}

	if err := library.DeletePublishedView(ctx, conn, pv.ViewName); err != nil {
		log.Error().Err(err).Msg("could not delete published view")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not delete published view",
		})
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// GetPublicationCandidates returns subscription tables eligible as sources.
func GetPublicationCandidates(c *fiber.Ctx) error {
	idStr := c.Params("id")

	id, err := uuid.Parse(idStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(HttpError{
			Code:    "400",
			Message: "invalid publication ID",
		})
	}

	myLibrary := getLibrary(c)
	ctx := c.UserContext()

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire database connection")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}

	defer conn.Release()

	pv, err := library.LoadPublishedViewByID(ctx, conn, id)
	if err != nil {
		log.Error().Err(err).Str("ID", idStr).Msg("published view not found")

		return c.Status(fiber.StatusNotFound).JSON(HttpError{
			Code:    "404",
			Message: "published view not found",
		})
	}

	subs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not load subscriptions")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not load subscriptions",
		})
	}

	existingTables := make(map[string]bool)
	for _, s := range pv.Sources {
		existingTables[s.TableName] = true
	}

	var candidates []CandidateSource

	for _, sub := range subs {
		tableName, ok := sub.DataTablesMap[pv.DataTypeKey]
		if !ok {
			continue
		}

		if existingTables[tableName] {
			continue
		}

		candidates = append(candidates, CandidateSource{
			TableName:        tableName,
			SubscriptionID:   sub.ID.String(),
			SubscriptionName: sub.Name,
			Provider:         sub.Provider,
			Dataset:          sub.Dataset,
		})
	}

	if candidates == nil {
		candidates = []CandidateSource{}
	}

	return c.JSON(candidates)
}

// GetAvailablePublicationTypes returns data types that can have new published views.
func GetAvailablePublicationTypes(c *fiber.Ctx) error {
	myLibrary := getLibrary(c)
	ctx := c.UserContext()

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire database connection")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not acquire database connection",
		})
	}

	defer conn.Release()

	views, err := library.LoadPublishedViews(ctx, conn)
	if err != nil {
		log.Error().Err(err).Msg("could not load published views")

		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code:    "500",
			Message: "could not load published views",
		})
	}

	existing := make(map[string]bool)
	for _, v := range views {
		existing[v.DataTypeKey] = true
	}

	var available []AvailableType

	for key, dt := range data.DataTypes {
		if dt.ViewName == "" || existing[key] {
			continue
		}

		available = append(available, AvailableType{
			DataTypeKey: key,
			ViewName:    dt.ViewName,
		})
	}

	if available == nil {
		available = []AvailableType{}
	}

	return c.JSON(available)
}

// enrichPublishedView converts a PublishedView into an EnrichedPublishedView
// by looking up subscription metadata for each source.
func enrichPublishedView(pv *library.PublishedView, subMap map[string]*library.Subscription) EnrichedPublishedView {
	sources := make([]EnrichedSource, len(pv.Sources))

	for i, s := range pv.Sources {
		es := EnrichedSource{
			TableName:      s.TableName,
			SubscriptionID: s.SubscriptionID,
		}

		if s.FromDate != nil {
			formatted := s.FromDate.Format("2006-01-02")
			es.FromDate = &formatted
		}

		if s.UntilDate != nil {
			formatted := s.UntilDate.Format("2006-01-02")
			es.UntilDate = &formatted
		}

		if sub, ok := subMap[s.SubscriptionID]; ok {
			es.SubscriptionName = sub.Name
			es.Provider = sub.Provider
			es.Dataset = sub.Dataset
		}

		sources[i] = es
	}

	overlaps := pv.CheckOverlaps()

	return EnrichedPublishedView{
		ID:          pv.ID,
		ViewName:    pv.ViewName,
		DataTypeKey: pv.DataTypeKey,
		Sources:     sources,
		Overlaps:    overlaps,
	}
}
```

- [ ] **Step 2: Register routes in route.go**

In `web/route.go`, add the publication routes after the quality routes (before the closing `}`):

```go
	api.Get("/publications", GetPublications)
	api.Get("/publications/available-types", GetAvailablePublicationTypes)
	api.Post("/publications", CreatePublication)
	api.Get("/publications/:id", GetPublication)
	api.Put("/publications/:id", UpdatePublication)
	api.Delete("/publications/:id", DeletePublication)
	api.Get("/publications/:id/candidates", GetPublicationCandidates)
```

Note: `/publications/available-types` must be registered before `/publications/:id` to avoid the `:id` param matching `available-types`.

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add web/handlers_publications.go web/route.go
git commit -m "feat(web): add publication API handlers and routes"
```

---

### Task 3: Backend handler tests

**Files:**
- Create: `web/handlers_publications_test.go`

- [ ] **Step 1: Write tests for enrichPublishedView and candidate filtering logic**

Since the handlers depend on database connections, we test the pure helper function `enrichPublishedView`. This function is unexported, so the test file uses `package web` (internal tests, matching existing web test patterns).

Create `web/handlers_publications_test.go`:

```go
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
package web

import (
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/library"
)

var _ = Describe("Publication Handlers", func() {
	Describe("enrichPublishedView", func() {
		It("enriches sources with subscription metadata", func() {
			subID := uuid.New()
			fromDate := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ID:          uuid.New(),
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{
						TableName:      "tiingo_eod_eod_abc12",
						SubscriptionID: subID.String(),
						FromDate:       &fromDate,
					},
				},
			}

			subMap := map[string]*library.Subscription{
				subID.String(): {
					ID:       subID,
					Name:     "Tiingo EOD",
					Provider: "tiingo",
					Dataset:  "eod",
				},
			}

			result := enrichPublishedView(pv, subMap)

			Expect(result.ID).To(Equal(pv.ID))
			Expect(result.ViewName).To(Equal("eod"))
			Expect(result.Sources).To(HaveLen(1))
			Expect(result.Sources[0].SubscriptionName).To(Equal("Tiingo EOD"))
			Expect(result.Sources[0].Provider).To(Equal("tiingo"))
			Expect(result.Sources[0].Dataset).To(Equal("eod"))
			Expect(*result.Sources[0].FromDate).To(Equal("2023-01-01"))
			Expect(result.Sources[0].UntilDate).To(BeNil())
		})

		It("handles missing subscription gracefully", func() {
			pv := &library.PublishedView{
				ID:          uuid.New(),
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{
						TableName:      "unknown_table",
						SubscriptionID: uuid.New().String(),
					},
				},
			}

			result := enrichPublishedView(pv, map[string]*library.Subscription{})

			Expect(result.Sources).To(HaveLen(1))
			Expect(result.Sources[0].SubscriptionName).To(Equal(""))
			Expect(result.Sources[0].Provider).To(Equal(""))
		})

		It("includes overlap warnings", func() {
			d1 := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
			d2 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			d3 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ID:          uuid.New(),
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "t1", SubscriptionID: "sub-1", FromDate: &d1, UntilDate: &d2},
					{TableName: "t2", SubscriptionID: "sub-2", FromDate: &d3},
				},
			}

			result := enrichPublishedView(pv, map[string]*library.Subscription{})

			Expect(result.Overlaps).To(HaveLen(1))
			Expect(result.Overlaps[0]).To(ContainSubstring("t1"))
		})

		It("returns no overlaps for clean sources", func() {
			boundary := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ID:          uuid.New(),
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "t1", SubscriptionID: "sub-1", UntilDate: &boundary},
					{TableName: "t2", SubscriptionID: "sub-2", FromDate: &boundary},
				},
			}

			result := enrichPublishedView(pv, map[string]*library.Subscription{})

			Expect(result.Overlaps).To(BeEmpty())
		})
	})
})
```

- [ ] **Step 2: Add a Ginkgo suite runner for the web package**

The web package has Ginkgo tests in `run_registry_test.go` that are bootstrapped by `observation_summary_test.go`. The new test file will be picked up by that existing suite runner. No new suite file needed.

- [ ] **Step 3: Run tests to verify they pass**

Run: `ginkgo run -race --focus "Publication Handlers" ./web/`
Expected: All tests PASS

- [ ] **Step 4: Commit**

```bash
git add web/handlers_publications_test.go
git commit -m "test(web): add unit tests for publication handler enrichment logic"
```

---

### Task 4: Frontend API functions

**Files:**
- Modify: `web/ui/src/lib/api.ts`

- [ ] **Step 1: Add publication API functions**

At the end of `web/ui/src/lib/api.ts`, add a new section:

```typescript
// ---------- Publications ----------

export async function getPublications() {
  const res = await authFetch('/publications')
  return handleResponse<any[]>(res)
}

export async function createPublication(body: { data_type_key: string }) {
  const res = await authFetch('/publications', {
    method: 'POST',
    body: JSON.stringify(body),
  })
  return handleResponse<any>(res)
}

export async function getPublication(id: string) {
  const res = await authFetch(`/publications/${id}`)
  return handleResponse<any>(res)
}

export async function updatePublication(id: string, body: { sources: any[] }) {
  const res = await authFetch(`/publications/${id}`, {
    method: 'PUT',
    body: JSON.stringify(body),
  })
  return handleResponse<any>(res)
}

export async function deletePublication(id: string) {
  const res = await authFetch(`/publications/${id}`, { method: 'DELETE' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ message: res.statusText }))
    throw new Error(body.message || `HTTP ${res.status}`)
  }
}

export async function getPublicationCandidates(id: string) {
  const res = await authFetch(`/publications/${id}/candidates`)
  return handleResponse<any[]>(res)
}

export async function getAvailablePublicationTypes() {
  const res = await authFetch('/publications/available-types')
  return handleResponse<any[]>(res)
}
```

- [ ] **Step 2: Commit**

```bash
git add web/ui/src/lib/api.ts
git commit -m "feat(ui): add publication API client functions"
```

---

### Task 5: Frontend routing and navigation

**Files:**
- Modify: `web/ui/src/router/index.ts`
- Modify: `web/ui/src/App.vue`

- [ ] **Step 1: Add publication routes**

In `web/ui/src/router/index.ts`, add 3 new routes after the `data-quality` route and before the `auth/callback` route:

```typescript
  {
    path: '/publications',
    name: 'publications',
    component: () => import('@/pages/PublicationsPage.vue'),
  },
  {
    path: '/publications/new',
    name: 'new-publication',
    component: () => import('@/pages/NewPublicationPage.vue'),
  },
  {
    path: '/publications/:id',
    name: 'publication-detail',
    component: () => import('@/pages/PublicationDetailPage.vue'),
  },
```

Note: `/publications/new` must come before `/publications/:id` in the routes array so it matches first.

- [ ] **Step 2: Add Publications to the navigation menubar**

In `web/ui/src/App.vue`, add a new menu item to the `menuItems` array, between "SQL Console" and "Data Quality":

```typescript
const menuItems = [
  { label: 'Subscriptions', icon: 'pi pi-database', command: () => router.push('/') },
  { label: 'SQL Console', icon: 'pi pi-code', command: () => router.push('/sql') },
  { label: 'Publications', icon: 'pi pi-book', command: () => router.push('/publications') },
  { label: 'Data Quality', icon: 'pi pi-check-circle', command: () => router.push('/data-quality') },
]
```

- [ ] **Step 3: Create placeholder pages so the app compiles**

Create `web/ui/src/pages/PublicationsPage.vue`:

```vue
<script setup lang="ts">
</script>

<template>
  <div>
    <h2>Publications</h2>
  </div>
</template>
```

Create `web/ui/src/pages/NewPublicationPage.vue`:

```vue
<script setup lang="ts">
</script>

<template>
  <div>
    <h2>New Publication</h2>
  </div>
</template>
```

Create `web/ui/src/pages/PublicationDetailPage.vue`:

```vue
<script setup lang="ts">
</script>

<template>
  <div>
    <h2>Publication Detail</h2>
  </div>
</template>
```

- [ ] **Step 4: Verify the frontend builds**

Run: `cd web/ui && npm run build`
Expected: Build succeeds

- [ ] **Step 5: Commit**

```bash
git add web/ui/src/router/index.ts web/ui/src/App.vue web/ui/src/pages/PublicationsPage.vue web/ui/src/pages/NewPublicationPage.vue web/ui/src/pages/PublicationDetailPage.vue
git commit -m "feat(ui): add publication routes, nav item, and placeholder pages"
```

---

### Task 6: Publications list page

**Files:**
- Modify: `web/ui/src/pages/PublicationsPage.vue`

- [ ] **Step 1: Implement the list page**

Replace the contents of `web/ui/src/pages/PublicationsPage.vue`:

```vue
<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Button from 'primevue/button'
import ProgressSpinner from 'primevue/progressspinner'
import Message from 'primevue/message'
import { getPublications } from '@/lib/api'

const router = useRouter()
const publications = ref<any[]>([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  try {
    publications.value = await getPublications()
  } catch (e: any) {
    error.value = e.message || 'Failed to load publications'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 1.5rem">
      <h2>Publications</h2>
      <Button label="New" icon="pi pi-plus" @click="router.push('/publications/new')" />
    </div>

    <div v-if="loading" style="display: flex; justify-content: center; padding: 2rem 0">
      <ProgressSpinner />
    </div>

    <Message v-else-if="error" severity="error" :closable="true" @close="error = ''">{{ error }}</Message>

    <DataTable v-else :value="publications" @row-click="(e: any) => router.push(`/publications/${e.data.id}`)">
      <Column field="view_name" header="View Name" style="cursor: pointer" />
      <Column field="data_type_key" header="Data Type" />
      <Column field="source_count" header="Sources" />
    </DataTable>
  </div>
</template>
```

- [ ] **Step 2: Verify the frontend builds**

Run: `cd web/ui && npm run build`
Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add web/ui/src/pages/PublicationsPage.vue
git commit -m "feat(ui): implement publications list page"
```

---

### Task 7: New publication page

**Files:**
- Modify: `web/ui/src/pages/NewPublicationPage.vue`

- [ ] **Step 1: Implement the new publication page**

Replace the contents of `web/ui/src/pages/NewPublicationPage.vue`:

```vue
<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Button from 'primevue/button'
import ProgressSpinner from 'primevue/progressspinner'
import Message from 'primevue/message'
import { getAvailablePublicationTypes, createPublication } from '@/lib/api'

const router = useRouter()
const types = ref<any[]>([])
const loading = ref(true)
const creating = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  try {
    types.value = await getAvailablePublicationTypes()
  } catch (e: any) {
    error.value = e.message || 'Failed to load available types'
  } finally {
    loading.value = false
  }
}

async function selectType(dataTypeKey: string) {
  creating.value = true
  error.value = ''
  try {
    const pv = await createPublication({ data_type_key: dataTypeKey })
    router.push(`/publications/${pv.id}`)
  } catch (e: any) {
    error.value = e.message || 'Failed to create publication'
    creating.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <Button label="Publications" icon="pi pi-arrow-left" text size="small" @click="router.push('/publications')" style="margin-bottom: 0.5rem" />
    <h2 style="margin-bottom: 1.5rem">New Publication</h2>

    <p style="margin-bottom: 1rem">Select a data type to create a published view for:</p>

    <div v-if="loading" style="display: flex; justify-content: center; padding: 2rem 0">
      <ProgressSpinner />
    </div>

    <Message v-else-if="error" severity="error" :closable="true" @close="error = ''">{{ error }}</Message>

    <div v-else-if="types.length === 0">
      <Message severity="info">All data types already have published views.</Message>
    </div>

    <DataTable
      v-else
      :value="types"
      :loading="creating"
      @row-click="(e: any) => selectType(e.data.data_type_key)"
      style="cursor: pointer"
    >
      <Column field="data_type_key" header="Data Type" />
      <Column field="view_name" header="View Name" />
    </DataTable>
  </div>
</template>
```

- [ ] **Step 2: Verify the frontend builds**

Run: `cd web/ui && npm run build`
Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add web/ui/src/pages/NewPublicationPage.vue
git commit -m "feat(ui): implement new publication page with data type picker"
```

---

### Task 8: Publication detail page

**Files:**
- Modify: `web/ui/src/pages/PublicationDetailPage.vue`

- [ ] **Step 1: Implement the detail page**

Replace the contents of `web/ui/src/pages/PublicationDetailPage.vue`:

```vue
<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Button from 'primevue/button'
import Dialog from 'primevue/dialog'
import InputText from 'primevue/inputtext'
import ProgressSpinner from 'primevue/progressspinner'
import Message from 'primevue/message'
import {
  getPublication,
  updatePublication,
  deletePublication,
  getPublicationCandidates,
} from '@/lib/api'

const route = useRoute()
const router = useRouter()
const id = computed(() => route.params.id as string)

const publication = ref<any>(null)
const loading = ref(true)
const error = ref('')
const saving = ref(false)

// Add source dialog
const showAddSource = ref(false)
const candidates = ref<any[]>([])
const loadingCandidates = ref(false)

// Edit dates dialog
const showEditDates = ref(false)
const editingSourceIdx = ref(-1)
const editFrom = ref('')
const editUntil = ref('')
const editDateError = ref('')

// Remove source dialog
const showRemoveSource = ref(false)
const removeSourceIdx = ref(-1)

// Delete view dialog
const showDeleteView = ref(false)

const isLastSource = computed(() =>
  publication.value && publication.value.sources.length === 1 && removeSourceIdx.value >= 0
)

async function load() {
  loading.value = true
  try {
    publication.value = await getPublication(id.value)
  } catch (e: any) {
    error.value = e.message || 'Failed to load publication'
  } finally {
    loading.value = false
  }
}

// --- Add source ---

async function openAddSource() {
  showAddSource.value = true
  loadingCandidates.value = true
  try {
    candidates.value = await getPublicationCandidates(id.value)
  } catch (e: any) {
    error.value = e.message || 'Failed to load candidates'
    showAddSource.value = false
  } finally {
    loadingCandidates.value = false
  }
}

async function addSource(candidate: any) {
  saving.value = true
  const newSource = {
    table_name: candidate.table_name,
    subscription_id: candidate.subscription_id,
  }
  const updatedSources = [
    ...publication.value.sources.map((s: any) => ({
      table_name: s.table_name,
      subscription_id: s.subscription_id,
      from_date: s.from_date ? new Date(s.from_date) : undefined,
      until_date: s.until_date ? new Date(s.until_date) : undefined,
    })),
    newSource,
  ]
  try {
    publication.value = await updatePublication(id.value, { sources: updatedSources })
    showAddSource.value = false
  } catch (e: any) {
    error.value = e.message || 'Failed to add source'
  } finally {
    saving.value = false
  }
}

// --- Edit dates ---

function openEditDates(idx: number) {
  editingSourceIdx.value = idx
  const source = publication.value.sources[idx]
  editFrom.value = source.from_date || ''
  editUntil.value = source.until_date || ''
  editDateError.value = ''
  showEditDates.value = true
}

function isValidDate(str: string): boolean {
  if (str === '') return true
  return /^\d{4}-\d{2}-\d{2}$/.test(str) && !isNaN(new Date(str).getTime())
}

function detectOverlaps(sources: any[]): string[] {
  if (sources.length <= 1) return []

  const sentinelMin = '1800-01-01'
  const sentinelMax = '2200-01-01'

  const items = sources.map((s: any) => ({
    name: s.table_name,
    from: s.from_date || sentinelMin,
    until: s.until_date || sentinelMax,
  }))

  items.sort((a: any, b: any) => a.from.localeCompare(b.from))

  const overlaps: string[] = []

  for (let i = 1; i < items.length; i++) {
    if (items[i].from < items[i - 1].until) {
      overlaps.push(
        `Overlapping: ${items[i - 1].name} [until ${items[i - 1].until}] and ${items[i].name} [from ${items[i].from}]`
      )
    }
  }

  return overlaps
}

async function saveEditDates() {
  if (!isValidDate(editFrom.value) || !isValidDate(editUntil.value)) {
    editDateError.value = 'Dates must be in YYYY-MM-DD format or empty'
    return
  }

  saving.value = true
  editDateError.value = ''

  const updatedSources = publication.value.sources.map((s: any, i: number) => {
    const source: any = {
      table_name: s.table_name,
      subscription_id: s.subscription_id,
    }

    if (i === editingSourceIdx.value) {
      if (editFrom.value) source.from_date = new Date(editFrom.value)
      if (editUntil.value) source.until_date = new Date(editUntil.value)
    } else {
      if (s.from_date) source.from_date = new Date(s.from_date)
      if (s.until_date) source.until_date = new Date(s.until_date)
    }

    return source
  })

  try {
    publication.value = await updatePublication(id.value, { sources: updatedSources })
    showEditDates.value = false
  } catch (e: any) {
    error.value = e.message || 'Failed to update dates'
  } finally {
    saving.value = false
  }
}

// --- Remove source ---

function openRemoveSource(idx: number) {
  removeSourceIdx.value = idx
  showRemoveSource.value = true
}

async function confirmRemoveSource() {
  saving.value = true

  if (publication.value.sources.length === 1) {
    try {
      await deletePublication(id.value)
      router.push('/publications')
    } catch (e: any) {
      error.value = e.message || 'Failed to delete publication'
      saving.value = false
    }

    return
  }

  const updatedSources = publication.value.sources
    .filter((_: any, i: number) => i !== removeSourceIdx.value)
    .map((s: any) => {
      const source: any = {
        table_name: s.table_name,
        subscription_id: s.subscription_id,
      }

      if (s.from_date) source.from_date = new Date(s.from_date)
      if (s.until_date) source.until_date = new Date(s.until_date)

      return source
    })

  try {
    publication.value = await updatePublication(id.value, { sources: updatedSources })
    showRemoveSource.value = false
  } catch (e: any) {
    error.value = e.message || 'Failed to remove source'
  } finally {
    saving.value = false
  }
}

// --- Delete view ---

async function confirmDeleteView() {
  saving.value = true
  try {
    await deletePublication(id.value)
    router.push('/publications')
  } catch (e: any) {
    error.value = e.message || 'Failed to delete publication'
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div v-if="loading" style="display: flex; justify-content: center; padding: 2rem 0">
      <ProgressSpinner />
    </div>

    <div v-else-if="error && !publication">
      <Message severity="error" :closable="true" @close="error = ''">{{ error }}</Message>
    </div>

    <template v-else-if="publication">
      <Button label="Publications" icon="pi pi-arrow-left" text size="small" @click="router.push('/publications')" style="margin-bottom: 0.5rem" />
      <h2 style="margin-bottom: 0.25rem">{{ publication.view_name }}</h2>
      <p style="margin-bottom: 1.5rem; opacity: 0.7">Data type: {{ publication.data_type_key }}</p>

      <Message v-if="error" severity="error" :closable="true" style="margin-bottom: 1rem" @close="error = ''">{{ error }}</Message>

      <Message v-for="(overlap, i) in (publication.overlaps || [])" :key="i" severity="warn" style="margin-bottom: 0.5rem">
        {{ overlap }}
      </Message>

      <!-- Sources table -->
      <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; margin-top: 1rem">
        <h3>Sources</h3>
        <Button label="Add Source" icon="pi pi-plus" size="small" @click="openAddSource" />
      </div>

      <DataTable :value="publication.sources" :loading="saving">
        <Column field="table_name" header="Source Table" />
        <Column field="subscription_name" header="Subscription" />
        <Column header="Provider / Dataset">
          <template #body="{ data }">{{ data.provider }} / {{ data.dataset }}</template>
        </Column>
        <Column field="from_date" header="From">
          <template #body="{ data }">{{ data.from_date || '' }}</template>
        </Column>
        <Column field="until_date" header="Until">
          <template #body="{ data }">{{ data.until_date || '' }}</template>
        </Column>
        <Column header="Actions" style="width: 120px">
          <template #body="{ index }">
            <Button icon="pi pi-pencil" text size="small" @click="openEditDates(index)" />
            <Button icon="pi pi-trash" text size="small" severity="danger" @click="openRemoveSource(index)" />
          </template>
        </Column>
      </DataTable>

      <div v-if="publication.sources.length === 0" style="text-align: center; padding: 2rem; opacity: 0.6">
        No sources yet. Click "Add Source" to get started.
      </div>

      <!-- Add source dialog -->
      <Dialog v-model:visible="showAddSource" header="Add Source" :modal="true" :style="{ width: '50rem' }">
        <div v-if="loadingCandidates" style="display: flex; justify-content: center; padding: 2rem 0">
          <ProgressSpinner />
        </div>
        <div v-else-if="candidates.length === 0">
          <Message severity="info">No eligible subscription tables found for this data type.</Message>
        </div>
        <DataTable v-else :value="candidates" @row-click="(e: any) => addSource(e.data)" style="cursor: pointer">
          <Column field="table_name" header="Table Name" />
          <Column field="subscription_name" header="Subscription" />
          <Column header="Provider / Dataset">
            <template #body="{ data }">{{ data.provider }} / {{ data.dataset }}</template>
          </Column>
        </DataTable>
      </Dialog>

      <!-- Edit dates dialog -->
      <Dialog v-model:visible="showEditDates" header="Edit Date Bounds" :modal="true" :style="{ width: '25rem' }">
        <div v-if="editingSourceIdx >= 0" style="margin-bottom: 1rem; opacity: 0.7">
          {{ publication.sources[editingSourceIdx]?.table_name }}
        </div>
        <div style="display: flex; flex-direction: column; gap: 1rem">
          <div>
            <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem">From date (YYYY-MM-DD or empty)</label>
            <InputText v-model="editFrom" placeholder="YYYY-MM-DD" style="width: 100%" />
          </div>
          <div>
            <label style="display: block; margin-bottom: 0.25rem; font-size: 0.875rem">Until date (YYYY-MM-DD or empty)</label>
            <InputText v-model="editUntil" placeholder="YYYY-MM-DD" style="width: 100%" />
          </div>
        </div>
        <Message v-if="editDateError" severity="error" style="margin-top: 0.5rem">{{ editDateError }}</Message>
        <template #footer>
          <Button label="Cancel" severity="secondary" @click="showEditDates = false" />
          <Button label="Save" :loading="saving" @click="saveEditDates" />
        </template>
      </Dialog>

      <!-- Remove source dialog -->
      <Dialog v-model:visible="showRemoveSource" header="Remove Source" :modal="true" :style="{ width: '30rem' }">
        <p v-if="removeSourceIdx >= 0">
          Remove <strong>{{ publication.sources[removeSourceIdx]?.table_name }}</strong> from this view?
        </p>
        <Message v-if="isLastSource" severity="warn" style="margin-top: 0.5rem">
          This is the last source -- the entire published view will be deleted.
        </Message>
        <template #footer>
          <Button label="Cancel" severity="secondary" @click="showRemoveSource = false" />
          <Button label="Remove" severity="danger" :loading="saving" @click="confirmRemoveSource" />
        </template>
      </Dialog>

      <!-- Delete view -->
      <Dialog v-model:visible="showDeleteView" header="Delete Published View" :modal="true" :style="{ width: '30rem' }">
        <p>Permanently delete the <strong>{{ publication.view_name }}</strong> published view and drop the database view? This cannot be undone.</p>
        <template #footer>
          <Button label="Cancel" severity="secondary" @click="showDeleteView = false" />
          <Button label="Delete" severity="danger" :loading="saving" @click="confirmDeleteView" />
        </template>
      </Dialog>

      <div style="margin-top: 2rem">
        <Button label="Delete View" icon="pi pi-trash" severity="danger" outlined @click="showDeleteView = true" />
      </div>
    </template>
  </div>
</template>
```

- [ ] **Step 2: Verify the frontend builds**

Run: `cd web/ui && npm run build`
Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add web/ui/src/pages/PublicationDetailPage.vue
git commit -m "feat(ui): implement publication detail page with source management"
```

---

### Task 9: Remove overlap error from SavePublishedView

**Files:**
- Modify: `library/published_views.go`

The spec says `SavePublishedView` should no longer reject overlapping date ranges. Currently `SavePublishedView` calls `ValidateSources()` which returns an error on overlap. We need to remove that call so overlaps are allowed. The overlap information is still available via `CheckOverlaps()` for the UI.

- [ ] **Step 1: Remove ValidateSources call from SavePublishedView**

In `library/published_views.go`, remove the `ValidateSources` call from `SavePublishedView`. Change the function from:

```go
func SavePublishedView(ctx context.Context, q Querier, pv *PublishedView) error {
	if err := pv.ValidateSources(); err != nil {
		return fmt.Errorf("validate sources: %w", err)
	}

	if err := ValidateSourceTables(ctx, q, pv); err != nil {
```

to:

```go
func SavePublishedView(ctx context.Context, q Querier, pv *PublishedView) error {
	if err := ValidateSourceTables(ctx, q, pv); err != nil {
```

- [ ] **Step 2: Update the test that checks overlap rejection**

In `library/published_views_test.go`, the test "rejects overlapping date ranges" tests `ValidateSources()` directly. That function still returns an error, so the test is still valid. No change needed.

- [ ] **Step 3: Run all library tests**

Run: `ginkgo run -race ./library/`
Expected: All tests PASS

- [ ] **Step 4: Commit**

```bash
git add library/published_views.go
git commit -m "feat(library): allow overlapping date ranges in SavePublishedView"
```

---

### Task 10: Full build verification

- [ ] **Step 1: Run Go lint**

Run: `make lint`
Expected: No errors (run `golangci-lint run --fix` if there are wsl/formatting issues)

- [ ] **Step 2: Run all Go tests**

Run: `make test`
Expected: All tests PASS

- [ ] **Step 3: Build the full binary (includes frontend)**

Run: `make build`
Expected: Build succeeds

- [ ] **Step 4: Commit any lint fixes if needed**

```bash
git add -A
git commit -m "style: lint fixes for publications feature"
```
