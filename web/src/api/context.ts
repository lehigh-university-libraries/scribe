import { createPromiseClient } from "@connectrpc/connect";
import { protoInt64 } from "@bufbuild/protobuf";
import { ContextService } from "../proto/scribe/v1/context_connect";
import {
  CreateContextRequest,
  CreateSelectionRuleRequest,
  DeleteContextRequest,
  DeleteSelectionRuleRequest,
  GetContextRequest,
  ListContextsRequest,
  ListSelectionRulesRequest,
  ResolveContextRequest,
  UpdateContextRequest,
  type Context,
  type ContextSelectionRule,
} from "../proto/scribe/v1/context_pb";
import { getTransport } from "./transport";

export interface ContextMetrics {
  context_id: number;
  total_runs: number;
  corrected_runs: number;
  avg_levenshtein_distance: number;
  avg_edit_count: number;
  avg_box_change_score: number;
}

function client() {
  return createPromiseClient(ContextService, getTransport());
}

export async function listContexts(systemOnly = false): Promise<Context[]> {
  const resp = await client().listContexts(new ListContextsRequest({ systemOnly }));
  return resp.contexts;
}

export async function getContext(contextId: string): Promise<Context> {
  const resp = await client().getContext(new GetContextRequest({ contextId: protoInt64.parse(contextId) }));
  if (!resp.context) throw new Error("no context in response");
  return resp.context;
}

export async function createContext(context: Context): Promise<Context> {
  const resp = await client().createContext(new CreateContextRequest({ context }));
  if (!resp.context) throw new Error("no context in response");
  return resp.context;
}

export async function updateContext(context: Context): Promise<Context> {
  const resp = await client().updateContext(new UpdateContextRequest({ context }));
  if (!resp.context) throw new Error("no context in response");
  return resp.context;
}

export async function deleteContext(contextId: string): Promise<void> {
  await client().deleteContext(new DeleteContextRequest({ contextId: protoInt64.parse(contextId) }));
}

export async function listSelectionRules(contextId = "0"): Promise<ContextSelectionRule[]> {
  const resp = await client().listSelectionRules(new ListSelectionRulesRequest({ contextId: protoInt64.parse(contextId) }));
  return resp.rules;
}

export async function createSelectionRule(rule: ContextSelectionRule): Promise<ContextSelectionRule> {
  const resp = await client().createSelectionRule(new CreateSelectionRuleRequest({ rule }));
  if (!resp.rule) throw new Error("no rule in response");
  return resp.rule;
}

export async function deleteSelectionRule(ruleId: string): Promise<void> {
  await client().deleteSelectionRule(new DeleteSelectionRuleRequest({ ruleId: protoInt64.parse(ruleId) }));
}

export async function resolveContext(metadataJson: string): Promise<{ context: Context; isDefault: boolean }> {
  const resp = await client().resolveContext(new ResolveContextRequest({ metadataJson }));
  if (!resp.context) throw new Error("no context in response");
  return { context: resp.context, isDefault: resp.isDefault };
}

export async function getContextMetrics(contextId: string): Promise<ContextMetrics> {
  const resp = await fetch(`/v1/contexts/${encodeURIComponent(contextId)}/metrics`);
  if (!resp.ok) {
    throw new Error(`metrics request failed: ${resp.status}`);
  }
  const body = await resp.json() as { metrics?: ContextMetrics };
  if (!body.metrics) {
    throw new Error("no metrics in response");
  }
  return body.metrics;
}
