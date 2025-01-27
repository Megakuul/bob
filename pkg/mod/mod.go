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
	Toolchain Toolchain `toml:"toolchain"`
	Targets map[string]Target `toml:"targets"`
	Includes []Include `toml:"includes"`
	Externals []External `toml:"externals"`
}

type COMPILER string
const (
	COMPILER_GCC COMPILER = "gcc"
	COMPILER_GPP COMPILER = "g++"
	COMPILER_CLANG COMPILER = "clang"
	COMPILER_CLANGPP COMPILER = "clang++"
	COMPILER_CLANGCL COMPILER = "clang-cl"
	COMPILER_MSVC COMPILER = "cl"
)

type Path struct {
	URL string `toml:"url"`
}

type Toolchain struct {
	Compiler string `toml:"compiler"`
	CompilerPath Path `toml:"compiler_path"`
	Linker string `toml:"linker"`
	LinkerPath Path `toml:"linker_path"`
	Std string `toml:"std"`
	StdPath Path `toml:"std_path"`
	StdStatic bool `toml:"std_static"`
}

type Target struct {
	Pack string `toml:"pack"`
	Library bool `toml:"library"`
}

type Include struct {
	Mod string `toml:"mod"`
	Version string `toml:"version"`
	Overlay string `toml:"overlay"`
}

type External struct {
	Name string `toml:"name"`
	Path Path `toml:"path"`
	Static bool `toml:"static"`
}
