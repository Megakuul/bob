package nixfmt

import "testing"

// Expected value from `nix hash convert --hash-algo sha256 --to nix32`.
func TestSRI(t *testing.T) {
	got, err := SRIToNix("sha256-20IVHZHefCQBPh2VtS1VVRQOeDNm14CzfuvbHz3Z70Q=")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sha256:0i7gv4yiznzbgsrq1mv66dw0w52malnvb58x7q0j8z6yj4fiahnv" {
		t.Fatal(got)
	}
	raw, err := DecodeBase32(got[7:], 32)
	if err != nil || Base32(raw) != got[7:] {
		t.Fatal("roundtrip", err)
	}
}
