package translate

import (
	"maps"
	"slices"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// Error codes the translator raises (SPEC §02.3). They are constants, so diag.New cannot meet
// an unknown code; newErr also fills a missing template argument with missingArg instead of letting
// diag.New panic (AGENTS.md §5.3). The catalogue replay test fails on any missingArg.
const (
	cNixpkgsUnbuildable diag.Code = 1010
	cBuildFailed        diag.Code = 2000
	cCompile            diag.Code = 2001
	cUndefinedSymbol    diag.Code = 2002
	cHeaderMissing      diag.Code = 2003
	cLibMissing         diag.Code = 2004
	cTestsFailed        diag.Code = 2005
	cGoTooOld           diag.Code = 2006
	cTimeout            diag.Code = 2007
	cSilent             diag.Code = 2008
	cNetwork            diag.Code = 2009
	cRustCrate          diag.Code = 2010
	cLogLimit           diag.Code = 2011
	cHashMismatch       diag.Code = 3001
	cDownloadFailed     diag.Code = 3002
	cDownloadNotFound   diag.Code = 3003
	cCacheAuth          diag.Code = 4001
	cCacheMissing       diag.Code = 4002
	cCacheUnreachable   diag.Code = 4003
	cCacheUntrusted     diag.Code = 4004
	cCacheCorrupt       diag.Code = 4005
	cCacheOnlyMiss      diag.Code = 4006
	cUploadFailed       diag.Code = 4007
	cUpstream           diag.Code = 5001
	cDiskFull           diag.Code = 6001
	cOOM                diag.Code = 6002
	cKilled             diag.Code = 6003
	cStoreAccess        diag.Code = 7002
	cStoreDamaged       diag.Code = 7003
	cStoreBusy          diag.Code = 7004
	cCancelled          diag.Code = 8001
	cBuilderBroken      diag.Code = 9001
	cNoOutput           diag.Code = 9002
	cForbiddenRef       diag.Code = 9003
	cBadStep            diag.Code = 9004
	cNixStopped         diag.Code = 9005
	cLogFormat          diag.Code = 9998
	cUnknown            diag.Code = 9999
)

// newErr returns an error of code with the template arguments args. A missing argument is
// filled with missingArg so that a translator bug degrades the message instead of crashing bob.
func newErr(code diag.Code, args map[string]string) *diag.Error {
	ent, ok := diag.Lookup(code)
	if !ok {
		ent, _ = diag.Lookup(cUnknown)
		code = cUnknown
	}
	kv := make([]string, 0, 2*len(args))
	for _, k := range slices.Sorted(maps.Keys(args)) {
		kv = append(kv, k, args[k])
	}
	// Only the selected variant's template must be complete (diag.New picks it by "variant").
	for _, v := range ent.Variants {
		if v.Name != args["variant"] && len(ent.Variants) > 1 {
			continue
		}
		for _, a := range diag.TemplateArgs(v.Message) {
			if _, ok := args[a]; !ok {
				kv = append(kv, a, missingArg)
			}
		}
	}
	return diag.New(code, kv...)
}

func hasCode(errs []*diag.Error, c diag.Code) bool {
	return slices.ContainsFunc(errs, func(e *diag.Error) bool { return e.Code == c })
}

func hasClass(errs []*diag.Error, c diag.Class) bool {
	return slices.ContainsFunc(errs, func(e *diag.Error) bool { return e.Code.Class() == c })
}

// missingArg fills a template argument a call site forgot (a translator bug).
const missingArg = "<missing>"
