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
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"golang.org/x/sync/errgroup"
)

type LOAD_TYPE int64
const (
	LOAD_GIT LOAD_TYPE = iota
	LOAD_HTTP
)

type job struct {
	typ LOAD_TYPE
	url string
	out string
	group *errgroup.Group
}

type Loader struct {
	rootCtx context.Context
	rootPath string

	jobsLock sync.Mutex
	jobs map[string]job
}

type LoaderOption func(*Loader)

func NewLoader(ctx context.Context, opts ...LoaderOption) *Loader {
	loader := &Loader{
		rootCtx: ctx,
		rootPath: "./.bobcache",
		jobsLock: sync.Mutex{},
		jobs: map[string]job{},
	}

	for _, opt := range opts {
		opt(loader)
	}

	return loader
}

// WithRootPath defines a custom root path where the loader will output downloaded data.
func WithRootPath(path string) LoaderOption {
	return func(l *Loader) {
		l.rootPath = path
	}
}

// Load() checks whether the requested asset is currently being downloaded. If this is the case, Load() waits
// until the download is complete. If not, Load() starts the download itself and waits until it is complete.
// The asset is extracted to $rootPath/$typ-$sha256(url)/...
func (l *Loader) Load(typ LOAD_TYPE, url string, clean bool) (string, error) {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d-%s", typ, url)))
	outputPath := filepath.Join(l.rootPath, string(hash[:]))

	l.jobsLock.Lock()
	activeJob, ok := l.jobs[string(hash[:])]
	if !ok {
		errGroup, _ := errgroup.WithContext(l.rootCtx)
		errGroup.Go(func() error {
			switch typ {
			case LOAD_GIT:
				return downloadGit(l.rootCtx, url, outputPath, clean)
			case LOAD_HTTP:
				return downloadHTTP(l.rootCtx, url, outputPath, clean)
			default:
				return fmt.Errorf("unsupported load type '%d'", typ)
			}
		})
		activeJob = job{typ: typ, url: url, out: outputPath, group: errGroup}
		l.jobs[string(hash[:])] = activeJob
	}
	l.jobsLock.Unlock()
	return outputPath, activeJob.group.Wait()
}

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

	// TODO unpack archive

	return nil
}

func prepare(path string, clean bool) (cached bool, err error) {
	if clean {
		if err:=os.RemoveAll(path); err!=nil {
			return false, err
		}
	}
	if _, err := os.Stat(path); err==nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}

	if err := os.MkdirAll(path, 0755); err!=nil {
		return false, err
	}
	return false, nil
}
