package cc

import ()

// registryEnv is the environment of a bob run with its own bob home.
func registryEnv(home string, extra ...string) []string {
	return append([]string{"BOB_HOME=" + home, "BOB_TEST_HTTP_LOOPBACK=1"}, extra...)
}
