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
package cmd

import (
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "pvdata",
	Short: "pvdata manages the investment database used by the Penny Vault family of tools",
	Long: `pv-data is a command line utility for building and maintaining a
database of price, fundamental, alternative, and various other types of data
useful in quantitative investing. Databases built by pv-data are used by the
penny-vault investment ecosystem to run backtests and perform live trading.

A key challenge in developing and executing quantitative investment strategies
is curating a reliable data library. There are many sources of investment data
including:

	* [Tiingo](https://www.tiingo.com)
	* [Nasdaq Data Link](https://data.nasdaq.com)
	* [Massive](https://massive.com)
	* custom datasets

Even though the data from each of these sources may be similar they all have
their own individual schema and method of obtaining data. pv-data solves these
challenges by maintaining a list of subscriptions and converting data from its
native schema into a format understood by penny-vault.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	// Default Playwright to headless. The published Docker image runs without
	// an X server; users debugging locally can opt back in via config.
	viper.SetDefault("playwright.headless", true)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.pvdata.toml)")
	infoCmd.PersistentFlags().String("dbUrl", "", "database connection string")

	if err := viper.BindPFlag("db.url", infoCmd.PersistentFlags().Lookup("dbUrl")); err != nil {
		log.Panic().Err(err).Msg("BindPFlag for db.url failed")
	}
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		// Search config in home directory with name ".pvdata" (without extension).
		viper.AddConfigPath(home)
		viper.SetConfigType("toml")
		viper.SetConfigName(".pvdata")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		log.Info().Str("ConfigFN", viper.ConfigFileUsed()).Msg("Using config file")
	}
}
