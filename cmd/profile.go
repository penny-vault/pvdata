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
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	cpuProfilePath   string
	memProfilePath   string
	blockProfilePath string
	mutexProfilePath string
	cpuProfileFile   *os.File
)

// registerProfileFlags wires profiling flags onto the given
// (typically root) command so any subcommand can be profiled.
// Profiles are started in a PersistentPreRunE and stopped in a
// PersistentPostRunE, with a SIGINT/SIGTERM handler that flushes the
// CPU profile before exit so a Ctrl-C on a slow import still produces
// a usable trace.
func registerProfileFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&cpuProfilePath, "cpu-profile", "", "write CPU profile to this file (analyse with `go tool pprof <file>`)")
	cmd.PersistentFlags().StringVar(&memProfilePath, "mem-profile", "", "write heap profile to this file on exit (analyse with `go tool pprof <file>`)")
	cmd.PersistentFlags().StringVar(&blockProfilePath, "block-profile", "", "write blocking profile to this file on exit (records goroutines parked on channel/network/syscall)")
	cmd.PersistentFlags().StringVar(&mutexProfilePath, "mutex-profile", "", "write mutex contention profile to this file on exit")
}

func startProfiling() error {
	// Block / mutex profiling has to be turned on before the events of
	// interest happen; the runtime samples them at a configurable rate.
	// Default rate of 1 records every blocking event, which is fine for
	// short diagnostic runs.
	if blockProfilePath != "" {
		runtime.SetBlockProfileRate(1)
	}

	if mutexProfilePath != "" {
		runtime.SetMutexProfileFraction(1)
	}

	needSignalHandler := blockProfilePath != "" || mutexProfilePath != "" || memProfilePath != ""

	if cpuProfilePath != "" {
		f, err := os.Create(cpuProfilePath)
		if err != nil {
			return fmt.Errorf("create cpu profile %s: %w", cpuProfilePath, err)
		}

		cpuProfileFile = f

		if err := pprof.StartCPUProfile(f); err != nil {
			_ = f.Close()
			cpuProfileFile = nil

			return fmt.Errorf("start cpu profile: %w", err)
		}

		log.Info().Str("path", cpuProfilePath).Msg("CPU profiling started")

		needSignalHandler = true
	}

	if needSignalHandler {
		installProfileSignalHandler()
	}

	return nil
}

func stopProfiling() {
	if cpuProfileFile != nil {
		pprof.StopCPUProfile()

		if err := cpuProfileFile.Close(); err != nil {
			log.Warn().Err(err).Msg("close cpu profile file")
		}

		log.Info().Str("path", cpuProfilePath).Msg("CPU profile written")

		cpuProfileFile = nil
	}

	if memProfilePath != "" {
		f, err := os.Create(memProfilePath)
		if err != nil {
			log.Warn().Err(err).Str("path", memProfilePath).Msg("create mem profile failed")
		} else {
			// GC first so the heap snapshot reflects live allocations
			// rather than garbage that's about to be collected.
			runtime.GC()

			if werr := pprof.WriteHeapProfile(f); werr != nil {
				log.Warn().Err(werr).Str("path", memProfilePath).Msg("write mem profile failed")
			} else {
				log.Info().Str("path", memProfilePath).Msg("heap profile written")
			}

			if cerr := f.Close(); cerr != nil {
				log.Warn().Err(cerr).Msg("close mem profile file")
			}
		}
	}

	writeNamedProfile("block", blockProfilePath)
	writeNamedProfile("mutex", mutexProfilePath)
}

// writeNamedProfile writes one of the runtime/pprof named profiles
// ("block", "mutex", "goroutine", etc.) to path. Used for sampling
// profiles whose state accumulates while the program runs and is
// captured at exit, in contrast to the streaming CPU profile.
func writeNamedProfile(name, path string) {
	if path == "" {
		return
	}

	p := pprof.Lookup(name)
	if p == nil {
		log.Warn().Str("name", name).Msg("named profile not registered; skipping")
		return
	}

	f, err := os.Create(path)
	if err != nil {
		log.Warn().Err(err).Str("path", path).Msg("create profile failed")
		return
	}

	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Warn().Err(cerr).Msg("close profile file")
		}
	}()

	if err := p.WriteTo(f, 0); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("write profile failed")
		return
	}

	log.Info().Str("name", name).Str("path", path).Msg("profile written")
}

// installProfileSignalHandler ensures Ctrl-C / SIGTERM flushes the
// active CPU profile and writes the heap profile before exit. Without
// this, a slow long-running import that the operator aborts produces
// a truncated, unreadable CPU profile.
func installProfileSignalHandler() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-ch
		log.Info().Str("signal", sig.String()).Msg("flushing profiles before exit")
		stopProfiling()
		// Re-raise: stop notifications, then exit with the standard
		// 128+signal status so callers see the abort as a signal exit.
		signal.Stop(ch)

		os.Exit(128 + int(sig.(syscall.Signal)))
	}()
}
