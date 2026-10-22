// Package promote verifies the staged outputs of succeeded jobs and promotes them into the
// binary cache (SPEC §6.6.4, R-REG-002, R-REG-152, R-REG-005), deletes the promoted staging
// by known key (R-STORE-105), and manages the cache signing keys and their rotation
// (R-REG-153). Only orchestrators run it; the seeds live in the control bucket.
package promote
