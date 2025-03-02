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

package file

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type FileArtifact struct {
	path string
}

type FileArtifactOption func(*FileArtifact)

func NewFileArtifact(path string, opts ...FileArtifactOption) *FileArtifact {
	pathHash := sha256.Sum256([]byte(path))
	artifact := &FileArtifact{
		identifier: fmt.Sprintf("file-%s", string(pathHash[:])),
		path: path,
	}

	for _, opt := range opts {
		opt(artifact)
	}

	return artifact
}

func (f *FileArtifact) Clean(cacheRoot string) error {
	return nil
}

func (f *FileArtifact) Load(ctx context.Context, cacheRoot string) (string, error) {
	return "", nil
}

func (f *FileArtifact) SHA256() (string, error) {
	hash := sha256.New()
	file, err := os.Open(f.path)
	if err!=nil {
		return "", err
	}
	_, err = io.Copy(hash, file)
	if err!=nil {
		return "", err
	}

	return string(hash.Sum(nil)), nil
}

func (f *FileArtifact) Symlink(rootPath string) (string, error) {
	stat, err := os.Stat(f.path)
	if err!=nil {
		return "", err
	}
	if stat.IsDir() {
		return "", fmt.Errorf("artifact is not a file: directories are not supported")
	}
	
	targetPath := filepath.Join(rootPath, filepath.Base(f.path))
	err = os.Symlink(f.path, targetPath)
	if err!=nil {
		return "", err
	}
	return targetPath, err
}
