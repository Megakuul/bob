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

type Toolchain struct {
	Compiler artifact.Artifact
	Linker artifact.Artifact
	Stdlib artifact.Artifact
	Stdpplib artifact.Artifact
	Supportlibs []artifact.Artifact
	Startfiles []artifact.Artifact
}

func createToolchain(toolchain *modcfg.Toolchain) (*Toolchain, error) {
	compiler, err := createArtifact(toolchain.Compiler)
	if err!=nil {
		return nil, fmt.Errorf("cannot create compiler artifact: %w", err)
	}

	linker, err := createArtifact(toolchain.Linker)
	if err!=nil {
		return nil, fmt.Errorf("cannot create linker artifact: %w", err)
	}

	stdlib, err := createArtifact(toolchain.Stdlib)
	if err!=nil {
		return nil, fmt.Errorf("cannot create stdlib artifact: %w", err)
	}

	stdpplib, err := createArtifact(toolchain.Stdpplib)
	if err!=nil {
		return nil, fmt.Errorf("cannot create std++lib artifact: %w", err)
	}

	supportlibs := []artifact.Artifact{}
	for _, lib := range toolchain.Supportlibs {
		artifact, err := createArtifact(lib)
		if err!=nil {
			return nil, fmt.Errorf("cannot create supportlib artifact: %w", err)
		}
		supportlibs = append(supportlibs, artifact)
	}

	startfiles := []artifact.Artifact{}
	for _, lib := range toolchain.Startfiles {
		artifact, err := createArtifact(lib)
		if err!=nil {
			return nil, fmt.Errorf("cannot create startfiles artifact: %w", err)
		}
		startfiles = append(startfiles, artifact)
	}
	
	return &Toolchain{
		Compiler: compiler,
		Linker: linker,
		Stdlib: stdlib,
		Stdpplib: stdpplib,
		Supportlibs: supportlibs,
		Startfiles: startfiles,
	}, nil
}
