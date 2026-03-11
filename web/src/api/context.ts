import { createClient } from "@connectrpc/connect";
import { ContextService } from "../proto/scribe/v1/context_connect";
import {
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
  return createClient(ContextService, getTransport());
}

export async function listContexts(systemOnly = false): Promise<Context[]> {
  const resp = await client().listContexts({ systemOnly });
  return resp.contexts;
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

export async function listSelectionRules(contextId = "0"): Promise<ContextSelectionRule[]> {
  const resp = await client().listSelectionRules({ contextId: BigInt(contextId) });
  return resp.rules;
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
