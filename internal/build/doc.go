// Package buildctl is the build controller of an orchestrator (SPEC §05.6.2, §05.7, §06
// §6.9.4–§6.9.5): it creates builds (webhook, API, remote, retry, rerun), plans their jobs
// through the fleet queue, follows the jobs to their results, verifies evaluation reports
// (pipeline match, R-PIPE-003), applies the concurrency rule (R-PIPE-005), keeps the build
// indexes (bidx, bpage, bbr, bnodes) and reports per-pipeline commit statuses. It also serves
// BuildService and JobService (§6.5.7).
//
// Workers import it for the job contract: the target selectors of a JobSpec (Selection),
// the pipeline matcher (Match) and the evaluation report (Report).
package buildctl
