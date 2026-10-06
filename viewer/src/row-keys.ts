/** Risk ids are only unique inside their section — a document can carry three
 *  separate `rsk_1` — so a key names the row, not the thing it points at.
 *  Encoded as JSON so no separator inside an id can make two rows share one. */
export const sectionKey = (id: string) => JSON.stringify(["section", id]);
export const riskKey = (section: string, id: string) => JSON.stringify(["risk", section, id]);
