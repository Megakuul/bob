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
	"path/filepath"
	"strings"

	"github.com/megakuul/bob/internal/mod/artifact"
	"github.com/megakuul/bob/internal/mod/artifact/file"
	modcfg "github.com/megakuul/bob/pkg/mod"
)

func createArtifact(path modcfg.Path) (artifact.Artifact, error) {
	urlSegments := strings.SplitN(path.URL, "://", 2)
	if len(urlSegments) < 2 {
		return nil, fmt.Errorf("invalid url: expected '<protocol>://<url>' got '%.22s...'", path.URL)
	}
	protocol, location := urlSegments[0], urlSegments[1]

	switch strings.ToLower(protocol) {
	case "file":
		return file.NewFileArtifact(filepath.Join(location, path.Path)), nil
	case "git":
		
	case "http":
	case "https":
	default:
		return nil, fmt.Errorf("unsupported protocol '%s'", protocol)
	}
}
