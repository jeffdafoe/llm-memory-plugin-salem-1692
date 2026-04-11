// Village API client — wraps fetch calls with auth

import { getToken } from "./auth";

function apiFetch(url: string, init?: RequestInit): Promise<Response> {
    const token = getToken();
    const headers: Record<string, string> = {
        ...(init?.headers as Record<string, string> || {}),
    };
    if (token) {
        headers["Authorization"] = `Bearer ${token}`;
    }
    return fetch(url, { ...init, headers });
}

export interface VillageMe {
    agent: string;
    canEdit: boolean;
}

export async function fetchVillageMe(): Promise<VillageMe | null> {
    try {
        const resp = await apiFetch("/api/village/me");
        if (resp.ok) {
            const data = await resp.json();
            return { agent: data.agent, canEdit: data.can_edit };
        }
    } catch {
        // unavailable
    }
    return null;
}

export interface VillageAgent {
    id: string;
    name: string;
    llmMemoryAgent: string;
    role: string;
    coins: number;
    isVirtual: boolean;
}

export async function fetchVillageAgents(): Promise<VillageAgent[]> {
    try {
        const resp = await apiFetch("/api/village/agents");
        if (resp.ok) {
            const data = await resp.json();
            return data.map((a: any) => ({
                id: a.id,
                name: a.name,
                llmMemoryAgent: a.llm_memory_agent,
                role: a.role,
                coins: a.coins,
                isVirtual: a.is_virtual,
            }));
        }
    } catch {
        // unavailable
    }
    return [];
}

export async function setObjectOwner(objectId: string, owner: string | null): Promise<boolean> {
    try {
        const resp = await apiFetch(`/api/village/objects/${objectId}/owner`, {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ owner }),
        });
        return resp.ok;
    } catch {
        return false;
    }
}
