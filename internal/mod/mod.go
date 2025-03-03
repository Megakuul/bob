/**
 * Bob Build System
 *
 * Copyright (C) 2025 Linus Ilian Moser <linus.moser@megakuul.ch>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package mod

import (
	"fmt"
	"log/slog"

	modcfg "github.com/megakuul/bob/pkg/mod"
)

type PLATFORM int64
const (
	PLATFORM_UNIX PLATFORM = iota
	PLATFORM_WINDOWS
)

var PLATFORMS = map[string]PLATFORM{
	"unix": PLATFORM_UNIX,
	"linux": PLATFORM_UNIX,
	"windows": PLATFORM_WINDOWS,
}

type ARCH int64
const (
	ARCH_AMD64 ARCH = iota
	ARCH_ARM64
)

var ARCHS = map[string]ARCH{
	"amd64": ARCH_AMD64,
	"arm64": ARCH_ARM64,
}

type Mod struct {
	Module string
	Toolchains map[string]Toolchain
	Targets map[string]Target
	Includes map[string]Include
	Externals map[string]External
}

// CreateMod loads and validates a configuration module into a internal Mod.
// Only toolchains compatible with the platform / arch are included.
func CreateMod(cfg *modcfg.Mod, platform PLATFORM, arch ARCH) (*Mod, error) {
	toolchains, err := getToolchains(cfg.Toolchains, platform, arch)
	if err!=nil {
		return nil, fmt.Errorf("failed to load toolchains: %w", err)
	}

	targets, err := getTargets(cfg.Targets, toolchains)
	if err!=nil {
		return nil, fmt.Errorf("failed to load targets: %w", err)
	}

	includes, err := getIncludes(cfg.Includes)
	if err!=nil {
		return nil, fmt.Errorf("failed to load includes: %w", err)
	}

	externals, err := getExternals(cfg.Externals)
	if err!=nil {
		return nil, fmt.Errorf("failed to load externals: %w", err)
	}

	return &Mod{
		Module: cfg.Module,
		Toolchains: toolchains,
		Targets: targets,
		Includes: includes,
		Externals: externals,
	}, nil
}

// getToolchains loads and validates all toolchains that match with the wanted platform & architecture.
func getToolchains(cfgChains []modcfg.Toolchain, platform PLATFORM, arch ARCH) (map[string]Toolchain, error) {
	chains := map[string]Toolchain{}
	for _, cfgChain := range cfgChains {
		ok, err := checkArch(cfgChain.Archs, arch)
		if err!=nil {
			return nil, err
		}
		if !ok {
			slog.Debug(fmt.Sprintf(
				"toolchain '%s' does not support current arch; skipping toolchain...", cfgChain.Name,
			))
			continue
		}

		ok, err = checkPlatform(cfgChain.Platforms, platform)
		if err!=nil {
			return nil, err
		}
		if !ok {
			slog.Debug(fmt.Sprintf(
				"toolchain '%s' does not support current platform; skipping toolchain...", cfgChain.Name,
			))
			continue
		}

		chain, err := createToolchain(&cfgChain)
		if err!=nil {
			slog.Warn(fmt.Sprintf("%v; skipping toolchain '%s'...", err, cfgChain.Name))
			continue
		}
		chains[cfgChain.Name] = *chain
	}

	return chains, nil
}

// checkArch checks if the specified architecture is compatible with the configuration archs.
func checkArch(cfgArchs []string, arch ARCH) (bool, error) {
	for _, cfgArch := range cfgArchs {
		cfgArchType, ok := ARCHS[cfgArch]
		if !ok {
			slog.Warn(fmt.Sprintf("unknown architecture '%s' in toolchain detected...", cfgArch))
			continue
		}
		if cfgArchType == arch {
			return true, nil
		}
	}
	return false, nil
}

// checkPlatform checks if the specified platform is compatible with the configuration platforms.
func checkPlatform(cfgPlatforms []string, platform PLATFORM) (bool, error) {
	for _, cfgPlatform := range cfgPlatforms {
		cfgPlatformType, ok := PLATFORMS[cfgPlatform]
		if !ok {
			slog.Warn(fmt.Sprintf("unknown platform '%s' in toolchain detected...", cfgPlatform))
			continue
		}
		if cfgPlatformType == platform {
			return true, nil
		}
	}
	return false, nil
}


// getTargets loads and validates all configured targets that are compatible with the loaded toolchains.
func getTargets(cfgTargets []modcfg.Target, toolchains map[string]Toolchain) (map[string]Target, error) {
	targets := map[string]Target{}
	for _, cfgTarget := range cfgTargets {
		target, err := createTarget(&cfgTarget, toolchains)
		if err!=nil {
			slog.Warn(fmt.Sprintf("%v; skipping target '%s'...", err, cfgTarget.Pack))
			continue
		}
		targets[cfgTarget.Pack] = *target
	}
	return targets, nil
}


// getIncludes loads and validates all configured includes.
func getIncludes(cfgIncludes []modcfg.Include) (map[string]Include, error) {
	includes := map[string]Include{}
	for _, cfgInclude := range cfgIncludes {
		include, err := createInclude(&cfgInclude)
		if err!=nil {
			slog.Warn(fmt.Sprintf("%v; skipping include '%s'...", err, cfgInclude.Mod))
			continue
		}
		includes[cfgInclude.Mod] = *include
	}
	return includes, nil
}

// getExternals loads and validates all configured externals.
func getExternals(cfgExternals []modcfg.External) (map[string]External, error) {
	externals := map[string]External{}
	for _, cfgExternal := range cfgExternals {
		external, err := createExternal(&cfgExternal)
		if err!=nil {
			slog.Warn(fmt.Sprintf("%v; skipping external '%s'...", err, cfgExternal.Name))
			continue
		}
		externals[cfgExternal.Name] = *external
	}
	return externals, nil
}

