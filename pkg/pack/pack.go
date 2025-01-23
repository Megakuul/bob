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

package pack

import (
	"github.com/BurntSushi/toml"
	"os"
)

func LoadPack(path string) (*Pack, error) {
	rawPack, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	pack := &Pack{}
	_, err = toml.Decode(string(rawPack), pack)
	if err != nil {
		return nil, err
	}

	return pack, nil
}

type STD_LIB string

const (
	STD_C11 STD_LIB = "11"
	STD_C14 STD_LIB = "14"
	STD_C17 STD_LIB = "17"
	STD_C20 STD_LIB = "20"
	STD_C23 STD_LIB = "23"
)

type Pack struct {
	Std           STD_LIB  `toml:"std"`
	CompilerFlags []string `toml:"compiler_flags"`
	Includes      []string `toml:"includes"`
	Sources       []string `toml:"sources"`
	Deps          []string `toml:"deps"`
}
