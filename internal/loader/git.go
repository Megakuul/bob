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
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// downloadGit clones a github repository by the specified url. The clone happens always over http even if
// the url uses 'git://' prefix for identification. The url must contain a '@<revision>' suffix that specifies
// either the soft tag name, or commit hash that should be checked out.
func downloadGit(ctx context.Context, url, out string, clean bool) error {
	cached, err := prepare(out, clean)
	if err!=nil {
		return err
	}
	if cached {
		return nil
	}

	urlSegments := strings.SplitN(url, "@", 2)
	if len(urlSegments) != 2 {
		return fmt.Errorf("expected 'git://<host>/<path>@<revision>' found %d '@' characters", len(urlSegments))
	}
	httpUrl := fmt.Sprintf("https://%s", strings.TrimPrefix("git://", urlSegments[0]))
	revision := urlSegments[1]

	repo, err := git.PlainCloneContext(ctx, out, false, &git.CloneOptions{URL: httpUrl})
	if err!=nil {
		return err
	}

	// check if $revision is a commit hash, if not, it is considered a lightweight tag.
	if !regexp.MustCompile("^[a-f0-9]{40}$").MatchString(revision) {
		tagRef, err := repo.Reference(plumbing.NewTagReferenceName(revision), true)
		if err!=nil {
			return err
		}
		revision = tagRef.Hash().String()
	}

	worktree, err := repo.Worktree()
	if err!=nil {
		return err
	}

	err = worktree.Checkout(&git.CheckoutOptions{
		Hash: plumbing.NewHash(revision),
	})
	if err!=nil {
		return err
	}

	return nil
}
