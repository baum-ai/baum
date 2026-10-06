export const contractSchemaDiffDefinition = { title: "Database schema" };
export const apiContractDefinition = { title: "API contract" };
export const architectureDefinition = { title: "Architecture" };
export const workflowDefinition = { title: "Workflow" };
export const accessModelDefinition = { title: "Access model" };
export const dataMigrationDefinition = { title: "Data migration" };
export const searchSchemaDefinition = { title: "Search schema" };
export const externalIntegrationDefinition = { title: "External integration" };
export const bugFixDefinition = { title: "Bug fix" };
export const refactorDefinition = { title: "Refactor" };
export const performanceDefinition = { title: "Performance" };
export const alternativeApproachDefinition = { title: "Alternative approach" };

/** The same titles, reachable by the schema kind a review states. */
export const tileTitles: Record<string, string> = {
  "database-schema": contractSchemaDiffDefinition.title,
  "api-contract": apiContractDefinition.title,
  architecture: architectureDefinition.title,
  "workflow-state-machine": workflowDefinition.title,
  "access-model": accessModelDefinition.title,
  "data-migration": dataMigrationDefinition.title,
  "search-schema": searchSchemaDefinition.title,
  "external-integration": externalIntegrationDefinition.title,
  "bug-fix": bugFixDefinition.title,
  refactor: refactorDefinition.title,
  performance: performanceDefinition.title,
  "alternative-approach": alternativeApproachDefinition.title,
};
