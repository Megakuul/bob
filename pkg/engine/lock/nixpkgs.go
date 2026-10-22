package lock

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/engine/fetch"
)

// DefaultChannel is the nixpkgs channel of a repository whose RepoOpts.Nixpkgs is empty.
const DefaultChannel = "nixos-26.05"

// NixpkgsSpec is the parsed RepoOpts.Nixpkgs: "<channel>" or "<channel>@<40-hex rev>".
type NixpkgsSpec struct {
	Channel string
	Rev     string // "" = the channel's current revision
}

// ParseNixpkgs parses RepoOpts.Nixpkgs ("" = DefaultChannel).
func ParseNixpkgs(s string) (NixpkgsSpec, error) {
	if s == "" {
		return NixpkgsSpec{Channel: DefaultChannel}, nil
	}
	ch, rev, hasRev := strings.Cut(s, "@")
	if !channelRe.MatchString(ch) {
		return NixpkgsSpec{}, fmt.Errorf("nixpkgs channel %q is invalid (e.g. %q)", clip(ch), DefaultChannel)
	}
	if hasRev && !hex40Re.MatchString(rev) {
		return NixpkgsSpec{}, fmt.Errorf("nixpkgs revision %q is not a 40-digit commit id", clip(rev))
	}
	return NixpkgsSpec{Channel: ch, Rev: rev}, nil
}

// NixpkgsURL is the archive bob fetches nixpkgs from.
func NixpkgsURL(rev string) string {
	return "https://github.com/NixOS/nixpkgs/archive/" + rev + ".tar.gz"
}

// NixpkgsRequest is the fetch request of a pinned nixpkgs source tree (unpacked, strip=1,
// verified against the pin's narHash).
func NixpkgsRequest(p NixpkgsPin) fetch.Request {
	return fetch.Request{URLs: []string{p.URL}, Kind: fetch.Archive, NarHash: p.NarHash,
		Name: "source", Strip: 1, Label: "nixpkgs"}
}

// ChannelRev returns the current revision of a nixpkgs channel from
// https://channels.nixos.org/<channel>/git-revision.
func ChannelRev(ctx context.Context, channel string) (string, error) {
	if !channelRe.MatchString(channel) {
		return "", fmt.Errorf("nixpkgs channel %q is invalid", clip(channel))
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://channels.nixos.org/"+channel+"/git-revision", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("nixpkgs channel %s: %w", channel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("nixpkgs channel %s: %s", channel, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", fmt.Errorf("nixpkgs channel %s: %w", channel, err)
	}
	rev := strings.TrimSpace(string(b))
	if !hex40Re.MatchString(rev) {
		return "", fmt.Errorf("nixpkgs channel %s: the server returned %q, not a commit id", channel, clip(rev))
	}
	return rev, nil
}

// PinNixpkgs returns the nixpkgs pin for the declaration decl (RepoOpts.Nixpkgs), R-NIX-005.
// The revision is decl's "@rev", else old's revision when old is a pin of the same channel and
// update is false (pins move only with bob lock --update nixpkgs), else the channel's current
// revision from channelRev (nil = ChannelRev). The source is downloaded once by f to record its
// narHash, unless old already pins that revision.
func PinNixpkgs(ctx context.Context, f fetch.Fetcher, decl string, old NixpkgsPin, update bool,
	channelRev func(context.Context, string) (string, error)) (NixpkgsPin, error) {
	spec, err := ParseNixpkgs(decl)
	if err != nil {
		return NixpkgsPin{}, err
	}
	inputs, err := Inputs(decl)
	if err != nil {
		return NixpkgsPin{}, err
	}
	rev := spec.Rev
	if rev == "" && !update && old.Channel == spec.Channel && hex40Re.MatchString(old.Rev) {
		rev = old.Rev
	}
	if rev == "" {
		if channelRev == nil {
			channelRev = ChannelRev
		}
		if rev, err = channelRev(ctx, spec.Channel); err != nil {
			return NixpkgsPin{}, err
		}
	}
	pin := NixpkgsPin{Channel: spec.Channel, Inputs: inputs, Rev: rev, URL: NixpkgsURL(rev)}
	if old.Rev == rev && old.URL == pin.URL && checkSRI("narHash", old.NarHash, "sha256") == nil {
		pin.NarHash = old.NarHash
		return pin, nil
	}
	req := NixpkgsRequest(pin)
	req.NarHash = ""
	_, narHash, err := f.Resolve(ctx, req)
	if err != nil {
		return NixpkgsPin{}, err
	}
	pin.NarHash = narHash
	return pin, nil
}
