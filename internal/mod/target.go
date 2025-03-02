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
	
	modcfg "github.com/megakuul/bob/pkg/mod"
)

type Target struct {
	Library bool
	Toolchain *Toolchain
}

func createTarget(target *modcfg.Target, toolchains map[string]Toolchain) (*Target, error) {
	output := &Target{
		Toolchain: nil,
		Library: target.Library,
	}
	
	for _, toolchain := range target.Toolchains {
		if chain, ok := toolchains[toolchain]; ok {
			output.Toolchain = &chain
			break
		}
	}

	if output.Toolchain == nil {
		return nil, fmt.Errorf("none of the toolchains '%v' is available", target.Toolchains)
	}
	return output, nil
}
