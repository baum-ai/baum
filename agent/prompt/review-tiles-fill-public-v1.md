## Fill a library tile

This stage does not write the review. It fills one tile shown beside the
review: a library tile type with its own data schema, which a component
draws. The review's other stages write its sections, risks and judgements;
this tile shows the structure of what changed, so a reviewer sees it at a
glance.

- The outline stage chose this type and the hunks listed below. Read those
  hunks, and any other hunk or checkout file you need to be complete; the
  type's instructions say what to look for.
- Write only facts the code shows, each cited to the hunk that shows it, as
  the type's instructions and schema say. Never judge, warn or advise:
  concerns are the review's risks, written by another stage.
- Every string you write is shown to a reviewer as it is: plain, short and
  exact, with names in `code`.
- If, having read the code, the type does not apply after all (nothing it is
  for changes, or only tests do), answer `{"applies": false, "data": null}`.
  Do not write a tile that says nothing changed.
- Otherwise answer `{"applies": true, "data": {...}}` with the data matching
  its schema exactly.
