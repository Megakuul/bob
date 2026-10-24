package worker

import _ "github.com/aws/aws-sdk-go-v2/service/s3" // want `import of github.com/aws/aws-sdk-go-v2/service/s3 is not allowed`
import _ "github.com/megakuul/lake"                // want `import of github.com/megakuul/lake is not allowed`
