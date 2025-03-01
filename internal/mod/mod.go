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
	"github.com/megakuul/bob/internal/mod/artifact"
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

type Toolchain struct {
	Compiler artifact.Artifact
	Linker artifact.Artifact
	Stdlib artifact.Artifact
	Stdpplib artifact.Artifact
	Supportlibs []artifact.Artifact
	Startfiles []artifact.Artifact
}

type Target struct {
	Pack string
	Library bool
	Toolchain *Toolchain
}

type Include struct {
	Mod artifact.Artifact
	Overlay artifact.Artifact
	RemoteToolchain bool
}

type External struct {
	RPaths []string
	Headers []artifact.Artifact
	Libraries []artifact.Artifact
}
