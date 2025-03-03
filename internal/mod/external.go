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

type External struct {
	RPaths []string
	Headers []Artifact
	Libraries []Artifact
}


func createExternal(external *modcfg.External) (*External, error) {
	headers := []Artifact{}
	for _, header := range external.Headers {
		artifact, err := createArtifact(header)
		if err!=nil {
			return nil, fmt.Errorf("cannot create header artifact: %w", err)
		}
		headers = append(headers, *artifact)
	}

	libraries := []Artifact{}
	for _, lib := range external.Libraries {
		artifact, err := createArtifact(lib)
		if err!=nil {
			return nil, fmt.Errorf("cannot create library artifact: %w", err)
		}
		libraries = append(libraries, *artifact)
	}
	
	return &External{
		RPaths: external.RPaths,
		Headers: headers,
		Libraries: libraries,
	}, nil
}
