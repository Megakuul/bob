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

package app

import (
	"github.com/megakuul/bob/cmd/bob/app/run"
	"github.com/megakuul/bob/cmd/bob/flags"
	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	options := NewRootOptions(flags.NewGlobalFlags())
	cmd := &cobra.Command{
		Use:          "bob",
		Short: "Bob Building System 🏗️ ",
		SilenceUsage: true,
	}
	options.globalFlags.AttachFlags(cmd.PersistentFlags())

	cmd.AddCommand(
		run.NewRunCmd(run.NewRunOptions(options.globalFlags)),
	)
	
	return cmd
}

type RootOptions struct {
	globalFlags *flags.GlobalFlags
}

func NewRootOptions(gFlags *flags.GlobalFlags) *RootOptions {
	return &RootOptions{
		globalFlags: gFlags,
	}
}
