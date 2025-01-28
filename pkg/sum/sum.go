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

package sum

import (
	"github.com/BurntSushi/toml"
	"os"
)

func LoadSum(path string) (*Sum, error) {
	rawSum, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	sum := &Sum{}
	_, err = toml.Decode(string(rawSum), sum)
	if err != nil {
		return nil, err
	}

	return sum, nil
}

type Sum struct {
	Toolchain Toolchain `toml:"toolchain"`
	Includes map[string]Include `toml:"includes"`
	Externals map[string]External `toml:"externals"`
}

type Toolchain struct {
	CompilerSHA256 string `toml:"compiler_sha256"`
	LinkerSHA256 string `toml:"linker_sha256"`
	StdSHA256 string `toml:"std_sha256"`
}

type Include struct {
	Version string `toml:"version"`
	SHA256 string `toml:"sha256"`
}

type External struct {
	SHA256 string `toml:"sha256"`
}
