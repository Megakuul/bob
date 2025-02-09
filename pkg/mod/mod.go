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
	"github.com/BurntSushi/toml"
	"os"
)

func LoadMod(path string) (*Mod, error) {
	rawMod, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	mod := &Mod{}
	_, err = toml.Decode(string(rawMod), mod)
	if err != nil {
		return nil, err
	}

	return mod, nil
}

type Mod struct {
	Module string `toml:"module"`
	Toolchains []Toolchain `toml:"toolchains"`
	Targets []Target `toml:"targets"`
	Includes []Include `toml:"includes"`
	Externals []External `toml:"externals"`
}

type Path struct {
	URL string `toml:"url"`
	Path string `toml:"path"`
	GitTag string `toml:"git_tag"`
	GitCommit string `toml:"git_commit"`
}

type Toolchain struct {
	Name string `toml:"name"`
	Platforms []string `toml:"platforms"`
	Archs []string `toml:"archs"`
	
	Compiler Path `toml:"compiler"`
	Linker Path `toml:"linker"`
	Stdlib Path `toml:"stdlib"`
	Stdpplib Path `toml:"stdpplib"`
	Supportlibs []Path `toml:"supportlibs"`
	Startfiles []Path `toml:"startfiles"`
}

type Target struct {
	Pack string `toml:"pack"`
	Library bool `toml:"library"`
	Toolchains []string `toml:"toolchains"`
}

type Include struct {
	Mod Path `toml:"mod"`
	RemoteToolchain bool `toml:"remote_toolchain"`
	Overlay string `toml:"overlay"`
}

type External struct {
	Name string `toml:"name"`
	RPaths []string `toml:"rpaths"`
	Headers []Path `toml:"headers"`
	Libraries []Path `toml:"libraries"`
}
