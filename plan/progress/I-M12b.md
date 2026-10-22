# I-M12b progress (M12.3 AppImage: pkg/output/appimage + gen + bop + runtime)

Owner of: pkg/output/appimage (API, gen, bop, runtime). Small additive changes elsewhere are
listed under "files".

## coordination with I-M12a (oci/tarball/release)
- Shared helpers I am adding (use them instead of writing your own, tell me if you need changes):
  - pkg/builder: `(*Ctx).Closure(attr)` reads an exportReferencesGraph list from
    $NIX_ATTRS_JSON_FILE (structured attrs); `builder.WriteLicences` writes licences.json
    (R-OUT-008) from the closure and a path→licence table.
  - pkg/engine/nixidx: Entry gains `Pname` and `Licenses` (SPDX ids from meta.license), so
    generators can fill the licence table at plan time (indexVersion bump).
- I will not touch pkg/output/oci, pkg/output/tarball, pkg/release.

## done
(starting 21:30)
