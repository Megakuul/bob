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

	"github.com/megakuul/bob/internal/mod/artifact"
	modcfg "github.com/megakuul/bob/pkg/mod"
)

func LoadMod(cfg modcfg.Mod, platform PLATFORM, arch ARCH) (*Mod, error) {
	toolchains, err := loadToolchains(cfg.Toolchains, platform, arch)
	if err!=nil {
		return nil, fmt.Errorf("failed to load toolchains: %w", err)
	}

	return mod, nil
}

func loadToolchains(cfgChains []modcfg.Toolchain, platform PLATFORM, arch ARCH) (map[string]Toolchain, error) {
	chains := map[string]Toolchain{}
	for _, cfgChain := range cfgChains {
		ok, err := checkArch(cfgChain.Archs, arch)
		if err!=nil {
			return nil, err
		}
		if !ok {
			continue
		}

		ok, err = checkPlatform(cfgChain.Platforms, platform)
		if err!=nil {
			return nil, err
		}
		if !ok {
			continue
		}

		_, ok = chains[cfgChain.Name]
		if ok {
			return nil, fmt.Errorf("toolchain with the name '%s' is specified twice", cfgChain.Name)
		}
		chains[cfgChain.Name] = Toolchain{
			
		}
	}

	return chains, nil
}

func checkArch(cfgArchs []string, arch ARCH) (bool, error) {
	for _, cfgArch := range cfgArchs {
		cfgArchType, ok := ARCHS[cfgArch]
		if !ok {
			return false, fmt.Errorf("unknown architecture '%s'... expected one of %v", cfgArch, ARCHS)
		}
		if cfgArchType == arch {
			return true, nil
		}
	}
	return false, nil
}

func checkPlatform(cfgPlatforms []string, platform PLATFORM) (bool, error) {
	for _, cfgPlatform := range cfgPlatforms {
		cfgPlatformType, ok := PLATFORMS[cfgPlatform]
		if !ok {
			return false, fmt.Errorf("unknown platform '%s'... expected one of %v", cfgPlatform, PLATFORMS)
		}
		if cfgPlatformType == platform {
			return true, nil
		}
	}
	return false, nil
}

func createArtifact(path modcfg.Path) (artifact.Artifact, error) {
	
}
