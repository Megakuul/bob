package promote

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/megakuul/bob/internal/cache"
)

// Output kinds whose derivations discard references by design (A149, SPEC §6.6.4): an image
// names its roots' store paths in its config (PATH, Entrypoint, R-OUT-012), an AppImage holds
// its interpreter path inside the zstd squashfs (R-OUT-015), a tarball the paths of its
// program. Such hashes are data, not references: the promotion does not register them and
// accepts them only when they belong to the job's own closure.
const (
	KindOCIImage = "oci.image"
	kindAppImage = "appimage."
	kindTarball  = "tarball."
)

var kindRE = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}\.[a-z][a-z0-9]{0,31}$`)

// DiscardsRefs reports whether outputs of kind hold store paths as data by design.
func DiscardsRefs(kind string) bool {
	return kindRE.MatchString(kind) &&
		(kind == KindOCIImage || strings.HasPrefix(kind, kindAppImage) || strings.HasPrefix(kind, kindTarball))
}

// maxTop bounds the top-level entries of a NAR remembered for the kind check.
const maxTop = 1024

// checkKind verifies the kind COMPLETE.json declares for path against what the orchestrator
// sees itself, so the relaxed scan never rests on the worker's word alone: the store name
// must be the one bob's generator of that kind gives its derivations (pkg/output: oci-image-,
// oci-layer-, appimage-, tarball-) and the NAR must have that kind's layout. "" = consistent.
func checkKind(path, kind string, top map[string]cache.NarEntry) string {
	name := strings.TrimPrefix(path, cache.StoreDir+"/")
	if i := strings.IndexByte(name, '-'); i >= 0 {
		name = name[i+1:]
	}
	regular := func(n string) bool { e, ok := top[n]; return ok && e.Type == "regular" }
	one := func(suffix string, exec bool) bool {
		n := 0
		for f, e := range top {
			if strings.HasSuffix(f, suffix) && e.Type == "regular" && (!exec || e.Executable) {
				n++
			}
		}
		return n == 1
	}
	bad := func(want string) string {
		return fmt.Sprintf("COMPLETE.json declares kind %s, but the path is not %s", kind, want)
	}
	switch {
	case kind == KindOCIImage && strings.HasPrefix(name, "oci-image-"):
		if !regular("oci-layout") || !regular("index.json") {
			return bad("an OCI image layout (oci-layout, index.json)")
		}
	case kind == KindOCIImage && strings.HasPrefix(name, "oci-layer-"):
		if !regular("layer.json") || !regular("layer.tar.gz") {
			return bad("an image layer (layer.json, layer.tar.gz)")
		}
	case kind == KindOCIImage:
		return bad("an oci-image- or oci-layer- output")
	case strings.HasPrefix(kind, kindAppImage):
		if !strings.HasPrefix(name, "appimage-") || !one(".AppImage", true) {
			return bad("an appimage- output with one executable *.AppImage")
		}
	case strings.HasPrefix(kind, kindTarball):
		if !strings.HasPrefix(name, "tarball-") || !one(".tar.gz", false) {
			return bad("a tarball- output with one *.tar.gz")
		}
	default:
		return fmt.Sprintf("COMPLETE.json declares kind %.40q, which does not discard references", kind)
	}
	return ""
}
