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
