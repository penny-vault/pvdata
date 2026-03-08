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
package playwright_helpers

import (
	"strings"

	"github.com/go-rod/stealth"
	"github.com/playwright-community/playwright-go"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

// StealthPage creates a new playwright page with stealth js loaded to prevent bot detection
func StealthPage(context *playwright.BrowserContext) playwright.Page {
	page, err := (*context).NewPage()
	if err != nil {
		log.Error().Err(err).Msg("could not create page")
	}

	if err = page.AddInitScript(playwright.Script{
		Content: playwright.String(stealth.JS),
	}); err != nil {
		log.Error().Err(err).Msg("could not load stealth mode")
	}

	return page
}

// BuildUserAgent dynamically determines the user agent and removes the headless identifier
func BuildUserAgent(browser *playwright.Browser) string {
	context, err := (*browser).NewContext()
	if err != nil {
		log.Error().Err(err).Msg("could not create context for building user agent")
	}
	defer context.Close()

	page, err := context.NewPage()
	if err != nil {
		log.Error().Err(err).Msg("could not create page BuildUserAgent")
	}

	resp, err := page.Goto("https://playwright.dev", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	})
	if err != nil {
		log.Error().Err(err).Str("Url", "https://playwright.dev").Msg("could not load page")
	}

	headers, err := resp.Request().AllHeaders()
	if err != nil {
		log.Error().Err(err).Msg("could not load request headers")
	}

	userAgent := headers["user-agent"]
	userAgent = strings.Replace(userAgent, "Headless", "", -1)
	return userAgent
}

// StartPlaywright starts the playwright server and browser, it then creates a new context and page with the stealth extensions loaded
func StartPlaywright(headless bool) (page playwright.Page, context playwright.BrowserContext, browser playwright.Browser, pw *playwright.Playwright) {
	pw, err := playwright.Run()
	if err != nil {
		log.Error().Err(err).Msg("could not launch playwright")
	}

	browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(headless),
	})
	if err != nil {
		log.Error().Err(err).Msg("could not launch Chromium")
	}

	log.Info().Bool("Headless", headless).Str("ExecutablePath", pw.Chromium.ExecutablePath()).Str("BrowserVersion", browser.Version()).Msg("starting playwright")

	// calculate user-agent
	userAgent := viper.GetString("user_agent")
	if userAgent == "" {
		userAgent = BuildUserAgent(&browser)
	}
	log.Info().Str("UserAgent", userAgent).Msg("using user-agent")

	// create context
	context, err = browser.NewContext(playwright.BrowserNewContextOptions{
		UserAgent: playwright.String(userAgent),
	})
	if err != nil {
		log.Error().Msg("could not create browser context")
	}

	// get a page
	page = StealthPage(&context)

	// block trackers
	BlockTrackers(page)

	return
}

func BlockTrackers(page playwright.Page) {
	// block a variety of domains that contain trackers and ads
	err := page.Route("**/*", func(route playwright.Route) {
		request := route.Request()
		if strings.Contains(request.URL(), "google.com") ||
			strings.Contains(request.URL(), "googletagservices.com") ||
			strings.Contains(request.URL(), "googlesyndication.com") ||
			strings.Contains(request.URL(), "facebook.com") ||
			strings.Contains(request.URL(), "moatpixel.com") ||
			strings.Contains(request.URL(), "moatads.com") ||
			strings.Contains(request.URL(), "adsystem.com") ||
			strings.Contains(request.URL(), "connatix.com") ||
			strings.Contains(request.URL(), "prebid") ||
			strings.Contains(request.URL(), "sodar") ||
			strings.Contains(request.URL(), "auction") ||
			strings.Contains(request.URL(), "rubiconproject.com") ||
			strings.Contains(request.URL(), "pubmatic.com") ||
			strings.Contains(request.URL(), "amazon-adsystem.com") ||
			strings.Contains(request.URL(), "adnxs.com") ||
			strings.Contains(request.URL(), "lijit.com") ||
			strings.Contains(request.URL(), "3lift.com") ||
			strings.Contains(request.URL(), "doubleclick.net") ||
			strings.Contains(request.URL(), "bidswitch.net") ||
			strings.Contains(request.URL(), "casalemedia.com") ||
			strings.Contains(request.URL(), "yahoo.com") ||
			strings.Contains(request.URL(), "sitescout.com") ||
			strings.Contains(request.URL(), "ipredictive.com") ||
			strings.Contains(request.URL(), "uat5-b.investingchannel.com") ||
			strings.Contains(request.URL(), "eyeota.net") {
			err := route.Abort("failed")
			if err != nil {
				log.Error().Err(err).Msg("failed blocking route")
			}
			return
		}

		/*
			if request.ResourceType() == "image" {
				err := route.Abort("failed")
				if err != nil {
					log.Error().Err(err).Msg("failed blocking image")
				}
			}
		*/

		if err := route.Continue(); err != nil {
			log.Error().Err(err).Msg("failed continueing route")
		}
	})

	if err != nil {
		log.Error().Err(err).Msg("page route errored")
	}
}

func StopPlaywright(page playwright.Page, context playwright.BrowserContext, browser playwright.Browser, pw *playwright.Playwright) {
	log.Info().Msg("closing browser")
	if err := browser.Close(); err != nil {
		log.Error().Err(err).Msg("error encountered when closing browser")
	}

	log.Info().Msg("stopping playwright")
	if err := pw.Stop(); err != nil {
		log.Error().Err(err).Msg("error encountered when stopping playwright")
	}
}
