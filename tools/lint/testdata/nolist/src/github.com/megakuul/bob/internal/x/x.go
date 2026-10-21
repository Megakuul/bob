package x

import (
	"example.com/dynamitedb"
	"example.com/s3"
)

func bad(c s3.Client) {
	c.ListObjectsV2()              // want `ListObjectsV2 outside`
	f := c.ListObjects             // want `ListObjects outside`
	s3.NewListObjectsV2Paginator() // want `NewListObjectsV2Paginator outside`
	dynamitedb.Query()             // want `dynamitedb.Query outside`
	c.GetObject()
	dynamitedb.Get()
	_ = f
}
