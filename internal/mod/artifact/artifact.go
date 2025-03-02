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

package artifact

import "context"

// Artifact abstracts a single arbitary file, independent of the underlying loader mechanism.
// If the artifact is stored remotely, it is loaded to a local cache on the initial call.
type Artifact interface {
	// Clean removes everything in $cacheRoot/<artifact-identifier>/*.
	Clean(cacheRoot string) error
	// Load loads the content of the artifact into $cacheRoot/<artifact-identifier>/.
	Load(ctx context.Context, cacheRoot string) (string, error)
	// Creates a SHA256 hash on the artifact.
	SHA256(cacheRoot string) (string, error)
	// Creates a symlink at "$linkRoot/filename". Returns "$linkRoot/filename" if successful.
	Symlink(linkRoot, cacheRoot string) (string, error)
}
