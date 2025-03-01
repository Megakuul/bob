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

package run

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/megakuul/bob/cmd/bob/flags"
	"github.com/megakuul/bob/internal/mod"
	modcfg "github.com/megakuul/bob/pkg/mod"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func NewRunCmd(options *RunOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "run",
		SilenceUsage: true,
		RunE: options.Run,
	}
	options.AttachFlags(cmd.Flags())

	return cmd
}

type RunOptions struct {
	globalFlags *flags.GlobalFlags
	clean bool
}

func NewRunOptions(gFlags *flags.GlobalFlags) *RunOptions {
	return &RunOptions{
		globalFlags: gFlags,
	}
}

func (r *RunOptions) AttachFlags(flagSet *pflag.FlagSet) {
	flagSet.BoolVarP(&r.clean,
		"cache", "c", false, "cleanup cache before execution") 
}

func (r *RunOptions) Run(cmd *cobra.Command, args []string) error {
	modPath, err := getMod()
	if err!=nil {
		return fmt.Errorf("cannot acquire bob mod: %w", err)
	}

	modCfg, err := modcfg.LoadMod(modPath)
	if err!=nil {
		return fmt.Errorf("cannot read bob mod: %w")
	}

	modPlatform, ok := mod.PLATFORMS[r.globalFlags.Platform]
	if !ok {
		return fmt.Errorf("unknown platform '%s'; use one of '%v'", r.globalFlags.Platform, mod.PLATFORMS)
	}

	modArch, ok := mod.ARCHS[r.globalFlags.Arch]
	if !ok {
		return fmt.Errorf("unknown architecture '%s'; use one of '%v'", r.globalFlags.Arch, mod.ARCHS)
	}

	mod, err := mod.LoadMod(modCfg, modPlatform, modArch)
	if err!=nil {
		return fmt.Errorf("cannot load bob mod: %w")
	}

	_ = mod

	return nil
}

// getMod performs a reverse directory traversal to find the current bob module.
func getMod() (string, error) {
	searchPath, err := filepath.Abs(".")
	if err!=nil {
		return "", fmt.Errorf("failed to read absolute directory path: %w", err)
	}
	for {
		entries, err := os.ReadDir(searchPath)
		if err!=nil {
			return "", fmt.Errorf("failed to reverse traverse directory: %w", err)
		}

		for _, entry := range entries {
			if entry.Name() == modcfg.MOD_FILE_NAME {
				return path.Join(searchPath, entry.Name()), nil
			}
		}

		if len(strings.Split(searchPath, string(filepath.Separator))) <= 2 {
			return "", fmt.Errorf("not inside a bob module...")
		}
		searchPath = filepath.Dir(searchPath)
	}
}
