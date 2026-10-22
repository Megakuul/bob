package forge

import "github.com/megakuul/bob/pkg/bob/diag"

func errNotImplemented(feature string) error { return diag.NotImplemented(feature) }
