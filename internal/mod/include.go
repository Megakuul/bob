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

type Include struct {
	Source Artifact
	RemoteToolchain bool
}

func createInclude(include *modcfg.Include) (*Include, error) {
	source, err := createArtifact(include.Source)
	if err!=nil {
		return nil, fmt.Errorf("cannot create source artifact: %w", err)
	}
	
	return &Include{
		Source: *source,
		RemoteToolchain: include.RemoteToolchain,
	}, nil
}
