package rust

import (
	"os"
)

// registryEnv points bob's resolver at the offline registry mirror (tools/regmirror, HARNESS
// §4.3) when the harness started one: BOB_TEST_CRATES_INDEX is the base URL of its sparse
// crates.io index (crate files are served next to it). The run then needs no network.
func registryEnv(home string, extra ...string) []string {
	env := []string{"BOB_HOME=" + home}
	if p := os.Getenv("BOB_TEST_CRATES_INDEX"); p != "" {
		env = append(env, "BOB_CRATES_INDEX="+p)
	}
	return append(env, extra...)
}
