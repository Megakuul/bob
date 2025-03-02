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

package http

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

type HttpArtifact struct {
	identifier string
	url string
}

type HttpArtifactOption func(*HttpArtifact)

func NewHttpArtifact(url string, opts ...HttpArtifactOption) *HttpArtifact {
	urlHash := sha256.Sum256([]byte(url))
	artifact := &HttpArtifact{
		identifier: fmt.Sprintf("http-%s", string(urlHash[:])),
		url: url,
	}

	for _, opt := range opts {
		opt(artifact)
	}

	return artifact
}

func (h *HttpArtifact) Clean(cacheRoot string) error {
	return os.RemoveAll(filepath.Join(cacheRoot, h.identifier))
}

func (h *HttpArtifact) Load(ctx context.Context, cacheRoot string) (string, error) {
	cachePath := filepath.Join(cacheRoot, h.identifier)
	if _, err := os.Stat(cachePath); err==nil {
		return cachePath, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	err := os.MkdirAll(cachePath, 0755)
	if err!=nil {
		return "", err
	}

	client := http.Client{}
	req, err := http.NewRequestWithContext(ctx, "GET", h.url, nil)
	if err!=nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err!=nil {
		return "", err
	}
	defer resp.Body.Close()

	archivePath := filepath.Join(cachePath, fmt.Sprintf("%s.download.blob", h.identifier))
	archive, err := os.Create(archivePath)
	if err!=nil {
		return "", err
	}
	_, err = io.Copy(archive, resp.Body)
	if err!=nil {
		return "", err
	}

	return "", nil
}

func (h *HttpArtifact) SHA256(cacheRoot string) (string, error) {
	hash := sha256.New()
	http, err := os.Open(h.path)
	if err!=nil {
		return "", err
	}
	_, err = io.Copy(hash, http)
	if err!=nil {
		return "", err
	}

	return string(hash.Sum(nil)), nil
}

func (h *HttpArtifact) Symlink(cacheRoot, linkRoot string) (string, error) {
	stat, err := os.Stat(h.path)
	if err!=nil {
		return "", err
	}
	if stat.IsDir() {
		return "", fmt.Errorf("artifact is not a http: directories are not supported")
	}
	
	targetPath := filepath.Join(rootPath, filepath.Base(h.path))
	err = os.Symlink(h.path, targetPath)
	if err!=nil {
		return "", err
	}
	return targetPath, err
}
