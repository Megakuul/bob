package sweep

import (
	"example.com/dynamitedb"
	"example.com/s3"
)

func sweep(c s3.Client) {
	c.ListObjectsV2()
	dynamitedb.Query()
}
