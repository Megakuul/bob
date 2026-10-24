package gen

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/megakuul/bob/pkg/output/appimage/bop"
)

// TestArgsMatchBuilder: the generator's job args decode strictly into the builder's schema and
// carry every field of it (the generator cannot import the builder layer).
func TestArgsMatchBuilder(t *testing.T) {
	x := args{Name: "api", Arch: "aarch64", Runtime: "/nix/store/r/bin/bobrun", Mksquashfs: "/nix/store/s/bin/mksquashfs",
		Entry: "/nix/store/a/bin/api", Entries: map[string]string{"curl": "/nix/store/c/bin/curl"}, Closure: closureAttr,
		CACert: "/nix/store/ca", Env: map[string]string{"A": "b"}, NoIconv: true, Locales: []string{"en_US.UTF-8"},
		Localedef: "/nix/store/g/bin/localedef", I18n: "/nix/store/g/share/i18n", HostGraphics: true, Mode: "userns",
		Prune: []string{"/nix/store/*-x-*/y"}, Desktop: desktop{Name: "API", Categories: []string{"Network"}, Terminal: true, Icon: "/nix/store/i/icon.svg"},
		Licences: map[string]licenceInfo{"/nix/store/a": {Name: "a", Version: "1", Licenses: []string{"MIT"}}}}
	b, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	var got bop.Args
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&got); err != nil {
		t.Fatalf("job args do not decode into bop.Args: %v", err)
	}
	back, _ := json.Marshal(got)
	if !bytes.Equal(back, b) {
		t.Errorf("bop.Args loses fields:\n gen %s\n bop %s", b, back)
	}
	if n, m := reflect.TypeOf(args{}).NumField(), reflect.TypeOf(bop.Args{}).NumField(); n != m {
		t.Errorf("gen args has %d fields, bop.Args %d", n, m)
	}
}
