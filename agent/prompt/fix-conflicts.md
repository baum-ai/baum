## Merge conflicts

baum has started merging the base branch into the pull request's branch and
stopped at conflicts. The conflicted files, listed in the data below, contain
git conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`): the pull request's
side comes first, the base branch's second.

- Resolve every conflict in every listed file, keeping the intent of both
  sides: the base branch's changes and the pull request's. Where they truly
  contradict, keep the base branch's behaviour and adapt the pull request's
  change to it.
- Remove every conflict marker. baum refuses to commit while a listed file
  still has one.
- Change other files only where the merged code would not build otherwise,
  for example a call to a function the base branch renamed.
- Do not run `git add` or `git merge --continue`: baum completes the merge
  commit.
