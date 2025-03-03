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

package processor

import "github.com/megakuul/bob/internal/mod"

type Processor struct {
	
}

type ProcessorOption func(*Processor)

func NewProcessor(opts ...ProcessorOption) *Processor {
	processor := &Processor{}

	for _, opt := range opts {
		opt(processor)
	}

	return processor
}


func (p *Processor) BuildTarget(module *mod.Mod, modPath string, target string) error {
	// 1. resolve target find all packs that must be processed and all includes / externals that must be present
	// 2. download and load all includes and perform step 1 adding all dependent includes etc.
	// 3. now we have a list of externals and a list of toolchains, download them.
	// 4. now we have a list of packs and external headers all downloaded. Compile them!
	// 4,5. compile every pack seperate with its depending headers and cache it anywhere.
	// 5. now we have various objectfiles / libs (compiled packs, external sources, toolchains) use the linker and link all of them in one fat big blob.
	// 6. pray to god that the linker does not do stupid linker things.
	// 7. output the final executable
	return nil
}
