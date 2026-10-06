/** The ids a tab and its panel share, so the strip and the panels it switches
 *  can be drawn apart and still name each other. */
export function tabIds(tablist: string, value: string) {
  return { tab: `${tablist}-tab-${value}`, panel: `${tablist}-panel-${value}` };
}

/** What a panel switched by a `tablist` strip carries. */
export function tabPanelProps(tablist: string, value: string, selected: boolean) {
  const ids = tabIds(tablist, value);
  return {
    role: "tabpanel",
    id: ids.panel,
    "aria-labelledby": ids.tab,
    hidden: !selected,
  } as const;
}
