package s3

type Client struct{}

func (Client) ListObjectsV2()    {}
func (Client) ListObjects()      {}
func (Client) GetObject()        {}
func NewListObjectsV2Paginator() {}
