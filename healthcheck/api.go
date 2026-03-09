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
package healthcheck

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/spf13/viper"
)

var (
	ErrStatus = errors.New("status code is invalid")
)

type createReq struct {
	APIKey      string `json:"api_key"`
	Name        string `json:"name"`
	Description string `json:"desc,omitempty"`
	Grace       int    `json:"grace"`
	Schedule    string `json:"schedule"`
	Slug        string `json:"slug"`
	Tags        string `json:"tags"`
	Timezone    string `json:"tz"`
}

type createResp struct {
	PingURL string `json:"ping_url"`
}

// Create a new healthchecks.io check and return the id
func Create(name string, slug string, tags []string, schedule string) (string, error) {
	command := createReq{
		APIKey:   viper.GetString("healthchecks.apikey"),
		Name:     name,
		Slug:     slug,
		Tags:     strings.Join(tags, " "),
		Grace:    3600,
		Schedule: schedule,
		Timezone: "America/New_York",
	}

	result := createResp{}

	client := resty.New()

	resp, err := client.R().
		SetHeader("Content-Type", "application/json").
		SetBody(command).
		SetResult(&result).
		Post("https://healthchecks.io/api/v3/checks/")
	if err != nil {
		return "", err
	}

	if resp.StatusCode() > 201 {
		return "", fmt.Errorf("%w: %d", ErrStatus, resp.StatusCode())
	}

	checkID := strings.Split(result.PingURL, "/")
	healthCheckID := checkID[len(checkID)-1]

	return healthCheckID, nil
}

// Pause monitoring of a health check
func Delete(id string) error {
	result := createResp{}

	client := resty.New()

	resp, err := client.R().
		SetHeader("Content-Type", "application/json").
		SetHeader("X-Api-Key", viper.GetString("healthchecks.apikey")).
		SetResult(&result).
		Delete(fmt.Sprintf("https://healthchecks.io/api/v3/checks/%s", id))
	if err != nil {
		return err
	}

	if resp.StatusCode() != 200 {
		return fmt.Errorf("%w: %d", ErrStatus, resp.StatusCode())
	}

	return nil
}

// Pause monitoring of a health check
func Pause(id string) error {
	result := createResp{}

	client := resty.New()

	resp, err := client.R().
		SetHeader("Content-Type", "application/json").
		SetHeader("X-Api-Key", viper.GetString("healthchecks.apikey")).
		SetResult(&result).
		Post(fmt.Sprintf("https://healthchecks.io/api/v3/checks/%s/pause", id))
	if err != nil {
		return err
	}

	if resp.StatusCode() != 200 {
		return fmt.Errorf("%w: %d", ErrStatus, resp.StatusCode())
	}

	return nil
}

// Resume monitoring of a health check
func Resume(id string) error {
	result := createResp{}

	client := resty.New()

	resp, err := client.R().
		SetHeader("Content-Type", "application/json").
		SetHeader("X-Api-Key", viper.GetString("healthchecks.apikey")).
		SetResult(&result).
		Post(fmt.Sprintf("https://healthchecks.io/api/v3/checks/%s/resume", id))
	if err != nil {
		return err
	}

	if resp.StatusCode() != 200 {
		return fmt.Errorf("%w: %d", ErrStatus, resp.StatusCode())
	}

	return nil
}
