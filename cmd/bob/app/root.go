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
	"github.com/spf13/cobra"
)

type cliFlags struct {
	debug bool
}

func NewRootCmd() *cobra.Command {
	flags := &cliFlags{}
	cmd := &cobra.Command{
		Use:          "bob",
		SilenceUsage: true,
		RunE: Run,
	}

	cmd.PersistentFlags().BoolVarP(&flags.debug,
		"debug", "d", false, "enable debug outputs")

	return cmd
}

func Run(cmd *cobra.Command, args []string) error {
	
}
