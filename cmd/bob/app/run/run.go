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
	"log/slog"
	"path/filepath"

	"github.com/megakuul/bob/cmd/bob/flags"
	"github.com/megakuul/bob/internal/mod"
	"github.com/megakuul/bob/internal/processor"
	modcfg "github.com/megakuul/bob/pkg/mod"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func NewRunCmd(options *RunOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "run",
		SilenceUsage: true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := options.Run(args); err!=nil {
				slog.Error(err.Error())
				return err
			}
			return nil
		},
	}
	options.AttachFlags(cmd.Flags())

	return cmd
}

type RunOptions struct {
	globalFlags *flags.GlobalFlags
	output string
	clean bool
}

func NewRunOptions(gFlags *flags.GlobalFlags) *RunOptions {
	return &RunOptions{
		globalFlags: gFlags,
	}
}

func (r *RunOptions) AttachFlags(flagSet *pflag.FlagSet) {
	flagSet.BoolVarP(&r.clean, "clean", "c", false, "cleanup cache before execution") 
}

func (r *RunOptions) Run(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("expected exactly '%d' argument got '%d'", 1, len(args))
	}
	target := args[0]
	
	modCfg, err := modcfg.LoadMod(r.globalFlags.Mod)
	if err!=nil {
		return fmt.Errorf("cannot read bob mod: %w", err)
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
		return fmt.Errorf("cannot load bob mod: %w", err)
	}

	proc := processor.NewProcessor()

	err = proc.BuildTarget(mod, target, filepath.Dir(r.globalFlags.Mod))
	if err!=nil {
		return err
	}

	return nil
}


