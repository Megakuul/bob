package x

import (
	_ "example.com/dynamitedb" // want `only internal/store imports dynamitedb`
	_ "github.com/megakuul/bob/internal/api"
)
