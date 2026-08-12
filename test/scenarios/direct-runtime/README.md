# Direct subprocess acceptance

This scenario proves that Crewfold can supervise a provider-free local process
without changing the deterministic task/run/handoff authority established by the
fake execution path.

It demonstrates:

- launch in the assigned checkout with an allowlisted environment;
- normalized progress, blockage/resume, accepted completion, and durable handoffs;
- rejection of a completion report whose evidence does not satisfy acceptance;
- bounded stdout/stderr with explicit omitted-byte counts;
- start failure, non-zero exit, timeout, graceful-stop fallback, and retained
  diagnostics;
- a child process continuing across daemon restart and reconciling to one result;
- refusal of a caller-supplied working-directory escape hatch.

Abort cleanup reads only runtime state inside the scenario's temporary data
directory and sends a forced signal only when `/proc/<pid>/cmdline` identifies the
exact scenario-owned binary. It never targets a workspace, repository, or broad
process group.

The fixture worker is a hidden mode of the checked-in Crewfold binary. It does not
call a model provider, access the network, or mutate the fixture repository.
