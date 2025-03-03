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
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/mholt/archives"
)

type LOAD_TYPE int64
const (
	LOAD_GIT LOAD_TYPE = iota
	LOAD_HTTP
	LOAD_FILE
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
			case LOAD_FILE:
				return downloadFile(l.rootCtx, url, outputPath, clean)
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

// prepare sets up the output directory for an asset. Specifying $clean ensures that the output directory
// is cleaned up first.
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

// unpack decompresses and extracts an archive to the $outputPath. It uses mholt/archives to detect and unpack
// the archive. Support depends on this library: https://github.com/mholt/archives#supported-archive-formats.
func unpack(ctx context.Context, archivePath, outputPath string) error {
	archive, err := os.Open(archivePath)
	if err!=nil {
		return err
	}
	defer archive.Close()

	format, reader, err := archives.Identify(ctx, filepath.Base(archivePath), archive)
	if err!=nil {
		return err
	}

	extractor, ok := format.(archives.Extractor)
	if !ok {
		return fmt.Errorf("format %s does not support extraction", format.MediaType())
	}

	err = extractor.Extract(ctx, reader, func(ctx context.Context, f archives.FileInfo) error {
		if f.IsDir() {
			return nil
		}

		outputPath := filepath.Join(outputPath, f.NameInArchive)
		err := os.MkdirAll(filepath.Dir(outputPath), 0755)
		if err!=nil {
			return err
		}
		
		inputFile, err := f.Open()
		if err!=nil {
			return err
		}
		defer inputFile.Close()

		outputFile, err := os.Create(outputPath)
		if err!=nil {
			return err
		}
		defer outputFile.Close()

		_, err = io.Copy(outputFile, inputFile)
		if err!=nil {
			return err
		}

		return nil
	})
	if err!=nil {
		return err
	}

	return nil
}
