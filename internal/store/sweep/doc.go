// Package sweep runs the cold sweeps, the only LIST calls of the server (R-STORE-101): only on
// the analytics and artifacts buckets, at most once per hour per controller, and every request
// carries the User-Agent suffix bob-sweep/<controller>.
package sweep
