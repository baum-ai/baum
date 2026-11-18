## Failing checks

The pull request's checks fail on its head commit. The failing checks, with
their summaries and the part of their logs that ends with the failed step,
are in the data below.

- Find the cause in the code and fix it. Fix the code rather than weaken,
  skip or delete a test, lint rule or check.
- A failure the pull request did not cause and cannot fix (an outage, a flaky
  test, a missing secret, an infrastructure error) needs no change: say so in
  your summary and change nothing.
- CI's `pull_request` checks build the merge of the pull request with its
  base branch, not its head alone. When a failure comes from that merge (code
  the base branch changed, which the head does not have yet), merge the base
  branch and fix the result; baum then pushes a merge commit. Merge only
  when the fix needs it.
- If you can run commands, run the project's own build, lint and test
  commands to check the fix. If you cannot, fix what the output shows and say
  in your summary what you could not check.
