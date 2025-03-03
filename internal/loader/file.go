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

package loader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// downloadFile loads a local fileurl to the output directory. If the fileurl points to a directory
// the directory is symlinked to $out, if it points to a file its considered an archive and extracted.
func downloadFile(ctx context.Context, url, out string, clean bool) error {
	cached, err := prepare(out, clean)
	if err!=nil {
		return err
	}
	if cached {
		return nil
	}

	filePath := strings.TrimPrefix(url, "file://")
	fileStat, err := os.Stat(filePath)
	if err!=nil {
		return err
	}

	// if the file is a directory, it is symlinked, if not it is considered an archive and unpacked.
	if fileStat.IsDir() {
		err = os.Remove(out)
		if err!=nil {
			return err
		}
		err = os.Symlink(filePath, out)
		if err!=nil {
			return fmt.Errorf("failed to symlink '.../%s': %w", filepath.Base(filePath), err)
		}
	} else {
		err = unpack(ctx, filePath, out)
		if err!=nil {
			return fmt.Errorf("failed to unpack '.../%s': %w", filepath.Base(filePath), err)
		}
	}
	return nil
}
