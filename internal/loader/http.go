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
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// downloadHTTP downloads an archive file and extracts it to the output location.
func downloadHTTP(ctx context.Context, url, out string, clean bool) error {
	cached, err := prepare(out, clean)
	if err!=nil {
		return err
	}
	if cached {
		return nil
	}

	client := http.Client{}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err!=nil {
		return err
	}
	resp, err := client.Do(req)
	if err!=nil {
		return err
	}
	defer resp.Body.Close()

	archivePath := filepath.Join(out, fmt.Sprintf("__internal.bob.blob"))
	archive, err := os.Create(archivePath)
	if err!=nil {
		return err
	}
	_, err = io.Copy(archive, resp.Body)
	if err!=nil {
		return err
	}

	err = unpack(ctx, archivePath, out)
	if err!=nil {
		return fmt.Errorf("failed to unpack '.../%s': %w", filepath.Base(archivePath), err)
	}

	return nil
}
