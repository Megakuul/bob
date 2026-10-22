//go:build !arm64 && !amd64

package evalbox

// Other architectures have no syscall filter yet; stage2 refuses to start (auditArch == 0).

const auditArch = 0

const (
	sysClone   = 0
	sysSocket  = 0
	sysPrctl   = 0
	sysSeccomp = 0
)

var deniedSyscalls []uint32
