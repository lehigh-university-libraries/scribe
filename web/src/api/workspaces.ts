import { createClient } from "@connectrpc/connect";
import { WorkspaceService } from "../proto/scribe/v1/workspace_connect";
import type {
  Workspace,
  WorkspaceAccess,
  WorkspaceMember,
} from "../proto/scribe/v1/workspace_pb";
import { getTransport } from "./transport";

function client() {
  return createClient(WorkspaceService, getTransport());
}

export async function listWorkspaces(): Promise<WorkspaceAccess[]> {
  const resp = await client().listWorkspaces({});
  return resp.workspaces;
}

export async function createWorkspace(name: string): Promise<WorkspaceAccess> {
  const resp = await client().createWorkspace({ name });
  if (!resp.workspace) {
    throw new Error("no workspace in response");
  }
  return resp.workspace;
}

export async function updateWorkspace(workspaceId: string | number | bigint, name: string): Promise<Workspace> {
  const resp = await client().updateWorkspace({ workspaceId: BigInt(workspaceId), name });
  if (!resp.workspace) {
    throw new Error("no workspace in response");
  }
  return resp.workspace;
}

export async function listWorkspaceMembers(workspaceId: string | number | bigint): Promise<{ workspace: Workspace; members: WorkspaceMember[] }> {
  const resp = await client().listWorkspaceMembers({ workspaceId: BigInt(workspaceId) });
  if (!resp.workspace) {
    throw new Error("no workspace in response");
  }
  return {
    workspace: resp.workspace,
    members: resp.members,
  };
}

export async function addWorkspaceMember(workspaceId: string | number | bigint, email: string, role: string): Promise<WorkspaceMember> {
  const resp = await client().addWorkspaceMember({ workspaceId: BigInt(workspaceId), email, role });
  if (!resp.member) {
    throw new Error("no member in response");
  }
  return resp.member;
}

export async function updateWorkspaceMember(workspaceId: string | number | bigint, userId: string | number | bigint, role: string): Promise<WorkspaceMember> {
  const resp = await client().updateWorkspaceMember({ workspaceId: BigInt(workspaceId), userId: BigInt(userId), role });
  if (!resp.member) {
    throw new Error("no member in response");
  }
  return resp.member;
}

export async function deleteWorkspaceMember(workspaceId: string | number | bigint, userId: string | number | bigint): Promise<void> {
  await client().deleteWorkspaceMember({ workspaceId: BigInt(workspaceId), userId: BigInt(userId) });
}
