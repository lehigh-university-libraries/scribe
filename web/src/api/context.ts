import { createClient } from "@connectrpc/connect";
import { ContextService } from "../proto/scribe/v1/context_connect";
import {
  type Context,
  type ContextSelectionRule,
  type GetModelCatalogResponse,
  type ContextMetrics,
  type ListContextsResponse,
  type ListSelectionRulesResponse,
} from "../proto/scribe/v1/context_pb";
import { getTransport } from "./transport";

export type { ContextMetrics };

function client() {
  return createClient(ContextService, getTransport());
}

export type ContextPage = Pick<ListContextsResponse, "contexts" | "nextPageToken">;
export type SelectionRulePage = Pick<ListSelectionRulesResponse, "rules" | "nextPageToken">;

export async function listContextPage(systemOnly = false, pageToken = "", pageSize = 100): Promise<ContextPage> {
  validateCatalogPage(pageSize, pageToken);
  const resp = await client().listContexts({ systemOnly, pageToken, pageSize });
  return { contexts: resp.contexts, nextPageToken: resp.nextPageToken };
}

// Preserve the convenient catalog helper while traversing only bounded server
// pages. Call listContextPage directly for incremental interfaces.
export async function listContexts(systemOnly = false): Promise<Context[]> {
  const contexts: Context[] = [];
  const seen = new Set<string>();
  let pageToken = "";
  do {
    const page = await listContextPage(systemOnly, pageToken);
    contexts.push(...page.contexts);
    pageToken = page.nextPageToken;
    if (pageToken && seen.has(pageToken)) throw new Error("context pagination returned a repeated token");
    if (pageToken) seen.add(pageToken);
  } while (pageToken);
  return contexts;
}

export async function getContext(contextId: string): Promise<Context> {
  const resp = await client().getContext({ contextId: BigInt(contextId) });
  if (!resp.context) throw new Error("no context in response");
  return resp.context;
}

export async function createContext(context: Context): Promise<Context> {
  const resp = await client().createContext({ context });
  if (!resp.context) throw new Error("no context in response");
  return resp.context;
}

export async function updateContext(context: Context): Promise<Context> {
  const resp = await client().updateContext({ context });
  if (!resp.context) throw new Error("no context in response");
  return resp.context;
}

export async function deleteContext(contextId: string): Promise<void> {
  await client().deleteContext({ contextId: BigInt(contextId) });
}

export async function listSelectionRulePage(contextId = "0", pageToken = "", pageSize = 100): Promise<SelectionRulePage> {
  validateCatalogPage(pageSize, pageToken);
  const resp = await client().listSelectionRules({ contextId: BigInt(contextId), pageToken, pageSize });
  return { rules: resp.rules, nextPageToken: resp.nextPageToken };
}

export async function listSelectionRules(contextId = "0"): Promise<ContextSelectionRule[]> {
  const rules: ContextSelectionRule[] = [];
  const seen = new Set<string>();
  let pageToken = "";
  do {
    const page = await listSelectionRulePage(contextId, pageToken);
    rules.push(...page.rules);
    pageToken = page.nextPageToken;
    if (pageToken && seen.has(pageToken)) throw new Error("selection rule pagination returned a repeated token");
    if (pageToken) seen.add(pageToken);
  } while (pageToken);
  return rules;
}

export async function createSelectionRule(rule: ContextSelectionRule): Promise<ContextSelectionRule> {
  const resp = await client().createSelectionRule({ rule });
  if (!resp.rule) throw new Error("no rule in response");
  return resp.rule;
}

export async function deleteSelectionRule(ruleId: string): Promise<void> {
  await client().deleteSelectionRule({ ruleId: BigInt(ruleId) });
}

export async function resolveContext(metadataJson: string): Promise<{ context: Context; isDefault: boolean }> {
  const resp = await client().resolveContext({ metadataJson });
  if (!resp.context) throw new Error("no context in response");
  return { context: resp.context, isDefault: resp.isDefault };
}

export async function getModelCatalog(): Promise<GetModelCatalogResponse> {
  return client().getModelCatalog({});
}

export async function getContextMetrics(contextId: string): Promise<ContextMetrics> {
  const resp = await client().getContextMetrics({ contextId: BigInt(contextId) });
  if (!resp.metrics) {
    throw new Error("no metrics in response");
  }
  return resp.metrics;
}

function validateCatalogPage(pageSize: number, pageToken: string): void {
  if (!Number.isInteger(pageSize) || pageSize < 1 || pageSize > 100) {
    throw new RangeError("pageSize must be an integer between 1 and 100");
  }
  if (pageToken.length > 512) {
    throw new RangeError("pageToken must contain at most 512 characters");
  }
}
